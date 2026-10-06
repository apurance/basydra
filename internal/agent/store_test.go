package agent

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"basydra/internal/protocol"
)

func TestHistorySurvivesRestartAndTornWrite(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 10; i++ {
		if err := s.Append(protocol.Snapshot{Version: 1, Timestamp: now.Add(time.Duration(i-10) * time.Second), CPUPercent: float64(i)}); err != nil {
			t.Fatal(err)
		}
	}
	history := s.History(now.Add(-time.Minute), 3)
	if len(history) != 3 || history[0].CPUPercent != 0 || history[2].CPUPercent != 9 {
		t.Fatalf("downsample lost endpoints: %+v", history)
	}
	s.Close()
	path := filepath.Join(dir, "samples-"+now.Format("2006-01-02")+".jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"timestamp":`)
	f.Close()
	s, err = OpenStore(dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.History(now.Add(-time.Hour), 100)) != 10 {
		t.Fatal("valid samples were lost on crash recovery")
	}
	if err = s.Append(protocol.Snapshot{Version: 1, Timestamp: now}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = OpenStore(dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if len(s.History(now.Add(-time.Hour), 100)) != 11 {
		t.Fatal("append after torn write was not recovered")
	}
}

func TestRetentionOnlyDeletesExpiredJournals(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().UTC().Add(-72 * time.Hour)
	data, _ := json.Marshal(protocol.Snapshot{Version: 1, Timestamp: old})
	oldPath := filepath.Join(dir, "samples-"+old.Format("2006-01-02")+".jsonl")
	os.WriteFile(oldPath, data, 0600)
	keepPath := filepath.Join(dir, "unrelated.txt")
	os.WriteFile(keepPath, []byte("keep"), 0600)
	s, err := OpenStore(dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatal("expired journal remains")
	}
	if _, err = os.Stat(keepPath); err != nil {
		t.Fatal("removed unrelated file")
	}
}

func TestProbeRequiresTokenAndRejectsStaleSnapshots(t *testing.T) {
	token := "a-long-random-test-token-123456789"
	p := &probe{interval: time.Second, tokenHash: sha256.Sum256([]byte(token)), latest: &protocol.Snapshot{Timestamp: time.Now().Add(-time.Hour)}}
	for _, tc := range []struct {
		token string
		want  int
	}{{"", 401}, {"wrong", 401}, {token, 503}} {
		r := httptest.NewRequest("GET", "/api/v1/snapshot", nil)
		if tc.token != "" {
			r.Header.Set("Authorization", "Bearer "+tc.token)
		}
		w := httptest.NewRecorder()
		p.authorize(p.snapshot)(w, r)
		if w.Code != tc.want {
			t.Fatalf("got %d want %d", w.Code, tc.want)
		}
	}
	p.latest.Timestamp = time.Now()
	r := httptest.NewRequest("GET", "/api/v1/snapshot", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	p.authorize(p.snapshot)(w, r)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code)
	}
}

func TestHistoryQueryLimits(t *testing.T) {
	for _, limit := range []string{"1", "2001", "2junk", "-5"} {
		if _, _, err := ParseHistoryQuery("", limit); err == nil {
			t.Fatalf("accepted %s", limit)
		}
	}
	if _, _, err := ParseHistoryQuery("not-a-date", "360"); err == nil {
		t.Fatal("accepted invalid since")
	}
}
