package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"

	"basydra/internal/protocol"
)

type View struct {
	Node
	Status      string             `json:"status"`
	Error       string             `json:"error,omitempty"`
	LastSuccess *time.Time         `json:"last_success,omitempty"`
	LatencyMS   int64              `json:"latency_ms"`
	Snapshot    *protocol.Snapshot `json:"snapshot,omitempty"`
}

type managedNode struct {
	node   Node
	ctx    context.Context
	cancel context.CancelFunc
	tunnel tunnel
	mu     sync.RWMutex
	view   View
}

type Manager struct {
	mu        sync.RWMutex
	ctx       context.Context
	path      string
	nodes     map[string]*managedNode
	interval  time.Duration
	semaphore chan struct{}
	http      *http.Client
	wg        sync.WaitGroup
}

func NewManager(ctx context.Context, path string, interval time.Duration) (*Manager, error) {
	c, err := LoadConfig(path)
	if err != nil {
		return nil, err
	}
	m := &Manager{ctx: ctx, path: path, nodes: map[string]*managedNode{}, interval: interval, semaphore: make(chan struct{}, 8), http: &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	for _, n := range c.Nodes {
		m.start(n)
	}
	return m, nil
}

// Caller holds manager write lock after startup.
func (m *Manager) start(n Node) {
	ctx, cancel := context.WithCancel(m.ctx)
	managed := &managedNode{node: n, ctx: ctx, cancel: cancel, view: View{Node: n.Public(), Status: "connecting"}}
	m.nodes[n.ID] = managed
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.poll(managed)
		ticker := time.NewTicker(m.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.poll(managed)
			}
		}
	}()
}

func (m *Manager) configLocked() Config {
	c := Config{Version: 1, Nodes: []Node{}}
	for _, n := range m.nodes {
		c.Nodes = append(c.Nodes, n.node)
	}
	sort.Slice(c.Nodes, func(i, j int) bool { return c.Nodes[i].ID < c.Nodes[j].ID })
	return c
}

func (m *Manager) Add(n Node) (Node, error) {
	if err := n.Validate(); err != nil {
		return Node{}, err
	}
	id, err := NewID()
	if err != nil {
		return Node{}, err
	}
	n.ID = id
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.nodes) >= 128 {
		return Node{}, fmt.Errorf("node limit reached (128)")
	}
	c := m.configLocked()
	c.Nodes = append(c.Nodes, n)
	if err = saveConfig(m.path, c); err != nil {
		return Node{}, fmt.Errorf("save config: %w", err)
	}
	m.start(n)
	return n.Public(), nil
}

func (m *Manager) Remove(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.nodes[id]
	if !ok {
		return fmt.Errorf("node not found")
	}
	c := m.configLocked()
	kept := []Node{}
	for _, item := range c.Nodes {
		if item.ID != id {
			kept = append(kept, item)
		}
	}
	c.Nodes = kept
	if err := saveConfig(m.path, c); err != nil {
		return err
	}
	delete(m.nodes, id)
	n.cancel()
	return nil
}

func (m *Manager) List() []View {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := []View{}
	for _, n := range m.nodes {
		n.mu.RLock()
		result = append(result, n.view)
		n.mu.RUnlock()
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (m *Manager) get(id string) (*managedNode, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	n, ok := m.nodes[id]
	return n, ok
}

func (m *Manager) fetch(ctx context.Context, n *managedNode, path string, out any) error {
	select {
	case m.semaphore <- struct{}{}:
		defer func() { <-m.semaphore }()
	case <-ctx.Done():
		return ctx.Err()
	}
	endpoint, err := n.tunnel.Endpoint(n.ctx, n.node)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+n.node.Token)
	response, err := m.http.Do(req)
	if err != nil {
		return fmt.Errorf("agent unreachable or request timed out")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("agent returned HTTP %d", response.StatusCode)
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(out); err != nil {
		return fmt.Errorf("invalid agent response")
	}
	return nil
}

func (m *Manager) poll(n *managedNode) {
	ctx, cancel := context.WithTimeout(n.ctx, 8*time.Second)
	defer cancel()
	start := time.Now()
	var snapshot protocol.Snapshot
	err := m.fetch(ctx, n, "/api/v1/snapshot", &snapshot)
	if err == nil && (snapshot.Version != protocol.Version || snapshot.Timestamp.IsZero() || snapshot.CPUPercent < 0 || snapshot.CPUPercent > 100 || snapshot.Memory.Total == 0) {
		err = fmt.Errorf("unsupported or invalid snapshot")
	}
	if err == nil && (time.Since(snapshot.Timestamp) > 5*time.Minute || time.Until(snapshot.Timestamp) > 5*time.Minute) {
		err = fmt.Errorf("agent clock is out of sync or sample is stale")
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if err != nil {
		n.view.Status = "offline"
		n.view.Error = err.Error()
		return
	}
	now := time.Now().UTC()
	n.view.Status = "online"
	n.view.Error = ""
	n.view.LatencyMS = time.Since(start).Milliseconds()
	n.view.LastSuccess = &now
	n.view.Snapshot = &snapshot
}

func (m *Manager) History(ctx context.Context, id, query string) (any, error) {
	n, ok := m.get(id)
	if !ok {
		return nil, fmt.Errorf("node not found")
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(n.ctx, cancel)
	defer stop()
	defer cancel()
	var result struct {
		Samples          []protocol.Snapshot `json:"samples"`
		RetentionSeconds float64             `json:"retention_seconds"`
	}
	if err := m.fetch(ctx, n, "/api/v1/history?"+query, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (m *Manager) Close() {
	m.mu.Lock()
	for _, n := range m.nodes {
		n.cancel()
	}
	m.mu.Unlock()
	m.wg.Wait()
	m.http.CloseIdleConnections()
}
