package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"basydra/internal/protocol"
)

const testToken = "random-test-token-with-32-characters"

func TestDefaultConfigPersistsAcrossWorkingDirectories(t *testing.T) {
	dir := t.TempDir()
	switch runtime.GOOS {
	case "windows":
		t.Setenv("APPDATA", dir)
	case "darwin", "ios":
		t.Setenv("HOME", dir)
	default:
		t.Setenv("XDG_CONFIG_HOME", dir)
	}
	userDir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	path, err := DefaultConfigPath()
	if err != nil || path != filepath.Join(userDir, "basydra", "client.json") || !filepath.IsAbs(path) {
		t.Fatalf("unexpected default configuration path: %q, %v", path, err)
	}
	node := Node{ID: "0123456789abcdef", Name: "test", URL: "http://127.0.0.1:19790", SSH: "ai", Token: testToken}
	if err := saveConfig(path, Config{Version: 1, Nodes: []Node{node}}); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	reopenedPath, err := DefaultConfigPath()
	if err != nil || reopenedPath != path {
		t.Fatalf("working directory changed default path: %q, %v", reopenedPath, err)
	}
	loaded, err := LoadConfig(reopenedPath)
	if err != nil || len(loaded.Nodes) != 1 || loaded.Nodes[0] != node {
		t.Fatalf("could not reload saved node from new working directory: %v", err)
	}
}

func TestNodeValidationProtectsTransportAndSSHArguments(t *testing.T) {
	for _, n := range []Node{
		{Name: "test", URL: "http://example.org", Token: testToken},
		{Name: "test", URL: "https://user:pass@example.org", Token: testToken},
		{Name: "test", URL: "http://127.0.0.1:19790", SSH: "-oProxyCommand=bad", Token: testToken},
		{Name: "test", URL: "http://example.org:19790", SSH: "ai", Token: testToken},
		{Name: "test", URL: "https://example.org/api", Token: testToken},
	} {
		if n.Validate() == nil {
			t.Fatalf("accepted unsafe node %+v", n.Public())
		}
	}
	n := Node{Name: "test", URL: "http://127.0.0.1:19790", SSH: "ai", Token: testToken}
	if err := n.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestManagerPersistsConfigRedactsTokensAndDetectsFailure(t *testing.T) {
	var failing atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			t.Error("missing bearer token")
		}
		if failing.Load() {
			w.WriteHeader(503)
			return
		}
		if r.URL.Path == "/api/v1/history" {
			json.NewEncoder(w).Encode(map[string]any{"samples": []protocol.Snapshot{}, "retention_seconds": 86400})
			return
		}
		json.NewEncoder(w).Encode(protocol.Snapshot{Version: 1, Timestamp: time.Now().UTC(), Memory: protocol.Memory{Total: 100, Used: 40, Percent: 40}, CPUPercent: 20})
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	path := filepath.Join(t.TempDir(), "config.json")
	m, err := NewManager(ctx, path, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	n, err := m.Add(Node{Name: "test", URL: server.URL, Token: testToken})
	if err != nil {
		t.Fatal(err)
	}
	if n.Token != "" {
		t.Fatal("add leaked token")
	}
	waitStatus(t, m, "online")
	encoded, _ := json.Marshal(m.List())
	if strings.Contains(string(encoded), testToken) || strings.Contains(string(encoded), `"token"`) {
		t.Fatal("list leaked token")
	}
	loaded, err := LoadConfig(path)
	if err != nil || len(loaded.Nodes) != 1 || loaded.Nodes[0].Token != testToken {
		t.Fatalf("config did not persist: %v", err)
	}
	if _, err = m.History(ctx, n.ID, "limit=2"); err != nil {
		t.Fatal(err)
	}
	failing.Store(true)
	waitStatus(t, m, "offline")
	if m.List()[0].Snapshot == nil {
		t.Fatal("last snapshot was lost")
	}
	if err = m.Remove(n.ID); err != nil {
		t.Fatal(err)
	}
	loaded, err = LoadConfig(path)
	if err != nil || len(loaded.Nodes) != 0 {
		t.Fatal("remove did not persist")
	}
}

func waitStatus(t *testing.T, m *Manager, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		views := m.List()
		if len(views) > 0 && views[0].Status == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("did not reach %s: %+v", want, m.List())
}

func TestFailedSaveDoesNotMutateRuntime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	m, err := NewManager(context.Background(), path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	os.Mkdir(path, 0700)
	if _, err = m.Add(Node{Name: "test", URL: "http://127.0.0.1:19790", Token: testToken}); err == nil {
		t.Fatal("save should have failed")
	}
	if len(m.List()) != 0 {
		t.Fatal("failed save changed active nodes")
	}
}

func TestLocalAPIBlocksCrossOriginAndDNSRebinding(t *testing.T) {
	h := localOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }), "17890")
	for _, tc := range []struct {
		host, origin, method, content string
		want                          int
	}{
		{"evil.example:17890", "", "GET", "", 403},
		{"127.0.0.1:17890", "https://evil.example", "POST", "application/json", 403},
		{"127.0.0.1:17890", "", "POST", "text/plain", 415},
		{"127.0.0.1:17890", "http://127.0.0.1:17890", "POST", "application/json", 200},
		{"localhost:17890", "", "GET", "", 200},
	} {
		r := httptest.NewRequest(tc.method, "http://"+tc.host+"/api/nodes", nil)
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("Content-Type", tc.content)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%+v got %d", tc, w.Code)
		}
	}
}
