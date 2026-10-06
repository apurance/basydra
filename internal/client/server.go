package client

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"basydra/internal/agent"
)

//go:embed web/*
var assets embed.FS

func Run(ctx context.Context, listen, config string, interval time.Duration) error {
	if interval < 2*time.Second || interval > time.Minute {
		return fmt.Errorf("poll interval must be 2s..1m")
	}
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("the MVP client must listen on a loopback IP")
	}
	m, err := NewManager(ctx, config, interval)
	if err != nil {
		return err
	}
	defer m.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/nodes", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, map[string]any{"nodes": m.List(), "poll_seconds": interval.Seconds()})
	})
	mux.HandleFunc("POST /api/nodes", func(w http.ResponseWriter, r *http.Request) {
		var n Node
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&n); err != nil {
			jsonError(w, http.StatusBadRequest, "invalid node JSON")
			return
		}
		if err := n.Validate(); err != nil {
			jsonError(w, http.StatusBadRequest, err.Error())
			return
		}
		added, err := m.Add(n)
		if err != nil {
			log.Printf("add node: %v", err)
			jsonError(w, http.StatusInternalServerError, "could not save node configuration")
			return
		}
		jsonResponse(w, http.StatusCreated, added)
	})
	mux.HandleFunc("DELETE /api/nodes/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if _, ok := m.get(id); !ok {
			jsonError(w, http.StatusNotFound, "node not found")
			return
		}
		if err := m.Remove(id); err != nil {
			log.Printf("remove node: %v", err)
			jsonError(w, http.StatusInternalServerError, "could not save configuration")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/nodes/{id}/history", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := m.get(r.PathValue("id")); !ok {
			jsonError(w, http.StatusNotFound, "node not found")
			return
		}
		since, limit, err := agent.ParseHistoryQuery(r.URL.Query().Get("since"), r.URL.Query().Get("limit"))
		if err != nil {
			jsonError(w, http.StatusBadRequest, err.Error())
			return
		}
		query := url.Values{"since": {since.Format(time.RFC3339)}, "limit": {strconv.Itoa(limit)}}
		result, err := m.History(r.Context(), r.PathValue("id"), query.Encode())
		if err != nil {
			jsonError(w, http.StatusBadGateway, err.Error())
			return
		}
		jsonResponse(w, http.StatusOK, result)
	})
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, map[string]any{"name": "Basydra", "version": "0.1.0"})
	})
	web, _ := fs.Sub(assets, "web")
	mux.Handle("GET /", http.FileServer(http.FS(web)))
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	server := &http.Server{Handler: localOnly(mux, port), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 8192}
	log.Printf("Basydra client at http://%s — config %s", listener.Addr(), config)
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err = server.Shutdown(shutdownCtx)
	case err = <-done:
	}
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func localOnly(next http.Handler, port string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, p, err := net.SplitHostPort(r.Host)
		ip := net.ParseIP(host)
		if err != nil || p != port || (host != "localhost" && (ip == nil || !ip.IsLoopback())) {
			http.Error(w, "invalid host", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host {
			http.Error(w, "invalid origin", http.StatusForbidden)
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" && r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "application/json required", http.StatusUnsupportedMediaType)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

func jsonResponse(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}
func jsonError(w http.ResponseWriter, status int, message string) {
	jsonResponse(w, status, map[string]string{"error": message})
}
