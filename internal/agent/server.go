package agent

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"basydra/internal/protocol"
)

type Options struct {
	Listen, Token, DataDir, TLSCert, TLSKey string
	Interval, Retention                     time.Duration
}

type probe struct {
	mu           sync.RWMutex
	latest       *protocol.Snapshot
	storageError bool
	store        *Store
	interval     time.Duration
	tokenHash    [32]byte
}

func Run(ctx context.Context, opts Options) error {
	if len(opts.Token) < 24 {
		return fmt.Errorf("agent token must contain at least 24 characters")
	}
	if opts.Interval < 2*time.Second || opts.Interval > time.Minute {
		return fmt.Errorf("interval must be 2s..1m")
	}
	if opts.Retention < time.Hour || opts.Retention > 72*time.Hour {
		return fmt.Errorf("retention must be 1h..72h")
	}
	host, _, err := net.SplitHostPort(opts.Listen)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if (ip == nil || !ip.IsLoopback()) && (opts.TLSCert == "" || opts.TLSKey == "") {
		return fmt.Errorf("a non-loopback listener requires --tls-cert and --tls-key")
	}
	store, err := OpenStore(opts.DataDir, opts.Retention)
	if err != nil {
		return err
	}
	defer store.Close()
	p := &probe{store: store, interval: opts.Interval, tokenHash: sha256.Sum256([]byte(opts.Token))}
	collector := &Collector{}
	if _, err = collector.Collect(); err != nil {
		return err
	}
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(opts.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-workerCtx.Done():
				return
			case <-ticker.C:
				sample, err := collector.Collect()
				if err != nil {
					log.Printf("collect: %v", err)
					continue
				}
				err = store.Append(sample)
				p.mu.Lock()
				p.latest = &sample
				p.storageError = err != nil
				p.mu.Unlock()
				if err != nil {
					log.Printf("history append: %v", err)
				}
			}
		}
	}()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", p.health)
	mux.HandleFunc("GET /api/v1/snapshot", p.authorize(p.snapshot))
	mux.HandleFunc("GET /api/v1/history", p.authorize(p.history))
	server := &http.Server{Addr: opts.Listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	listener, err := net.Listen("tcp", opts.Listen)
	if err != nil {
		cancel()
		<-done
		return err
	}
	log.Printf("Basydra agent listening on %s (sample %s; local history %s)", listener.Addr(), opts.Interval, opts.Retention)
	serveDone := make(chan error, 1)
	go func() {
		if opts.TLSCert != "" || opts.TLSKey != "" {
			serveDone <- server.ServeTLS(listener, opts.TLSCert, opts.TLSKey)
		} else {
			serveDone <- server.Serve(listener)
		}
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		err = server.Shutdown(shutdownCtx)
	case err = <-serveDone:
	}
	cancel()
	<-done
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (p *probe) authorize(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		provided := sha256.Sum256([]byte(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")))
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || subtle.ConstantTimeCompare(provided[:], p.tokenHash[:]) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		next(w, r)
	}
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(value)
}
func (p *probe) health(w http.ResponseWriter, r *http.Request) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	ready := p.latest != nil && time.Since(p.latest.Timestamp) < 3*p.interval
	w.Header().Set("Content-Type", "application/json")
	if !ready {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	writeJSON(w, map[string]any{"version": protocol.Version, "ready": ready, "history_writable": !p.storageError})
}
func (p *probe) snapshot(w http.ResponseWriter, r *http.Request) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.latest == nil || time.Since(p.latest.Timestamp) > 3*p.interval {
		http.Error(w, "metrics not ready or stale", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, p.latest)
}
func (p *probe) history(w http.ResponseWriter, r *http.Request) {
	since, limit, err := ParseHistoryQuery(r.URL.Query().Get("since"), r.URL.Query().Get("limit"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"samples": p.store.History(since, limit), "retention_seconds": p.store.retention.Seconds()})
}
