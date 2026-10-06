package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"basydra/internal/protocol"
)

// Store is node-local history. The client never persists metric samples.
type Store struct {
	mu        sync.RWMutex
	dir       string
	retention time.Duration
	samples   []protocol.Snapshot
	file      *os.File
	day       string
}

func OpenStore(dir string, retention time.Duration) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, retention: retention, samples: []protocol.Snapshot{}}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	cutoff := time.Now().UTC().Add(-retention)
	for _, entry := range entries {
		day, ok := journalDay(entry.Name())
		if !ok || entry.IsDir() {
			continue
		}
		if !day.Add(24 * time.Hour).After(cutoff) {
			if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
				return nil, err
			}
			continue
		}
		f, err := os.Open(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 4096), 1024*1024)
		for scanner.Scan() {
			var sample protocol.Snapshot
			// A torn last append after a crash is ignored on recovery.
			if json.Unmarshal(scanner.Bytes(), &sample) == nil && sample.Version == protocol.Version && sample.Timestamp.After(cutoff) {
				s.samples = append(s.samples, sample)
			}
		}
		err = scanner.Err()
		f.Close()
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(s.samples, func(i, j int) bool { return s.samples[i].Timestamp.Before(s.samples[j].Timestamp) })
	return s, nil
}

func journalDay(name string) (time.Time, bool) {
	if !strings.HasPrefix(name, "samples-") || !strings.HasSuffix(name, ".jsonl") {
		return time.Time{}, false
	}
	d, err := time.Parse("2006-01-02", strings.TrimSuffix(strings.TrimPrefix(name, "samples-"), ".jsonl"))
	return d, err == nil
}

func (s *Store) Append(sample protocol.Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	day := sample.Timestamp.UTC().Format("2006-01-02")
	if s.file == nil || s.day != day {
		if s.file != nil {
			if err := s.file.Close(); err != nil {
				return err
			}
			s.file = nil
		}
		f, err := os.OpenFile(filepath.Join(s.dir, "samples-"+day+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		// Separate an incomplete final record left by an interrupted write.
		if info, err := f.Stat(); err != nil {
			f.Close()
			return err
		} else if info.Size() > 0 {
			if _, err = f.WriteString("\n"); err != nil {
				f.Close()
				return err
			}
		}
		s.file, s.day = f, day
		entries, err := os.ReadDir(s.dir)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			d, ok := journalDay(entry.Name())
			if ok && !entry.IsDir() && !d.Add(24*time.Hour).After(sample.Timestamp.Add(-s.retention)) {
				if err := os.Remove(filepath.Join(s.dir, entry.Name())); err != nil {
					return err
				}
			}
		}
	}
	data, err := json.Marshal(sample)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if _, err = s.file.Write(data); err != nil {
		return err
	}
	if err = s.file.Sync(); err != nil {
		return err
	}
	s.samples = append(s.samples, sample)
	cutoff := sample.Timestamp.Add(-s.retention)
	start := sort.Search(len(s.samples), func(i int) bool { return !s.samples[i].Timestamp.Before(cutoff) })
	if start > 0 {
		s.samples = append([]protocol.Snapshot(nil), s.samples[start:]...)
	}
	return nil
}

func (s *Store) History(since time.Time, limit int) []protocol.Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	start := sort.Search(len(s.samples), func(i int) bool { return !s.samples[i].Timestamp.Before(since) })
	all := s.samples[start:]
	if len(all) <= limit {
		return append([]protocol.Snapshot{}, all...)
	}
	// Uniformly sample the requested interval, including both endpoints.
	result := make([]protocol.Snapshot, limit)
	for i := range result {
		result[i] = all[i*(len(all)-1)/(limit-1)]
	}
	return result
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file != nil {
		return s.file.Close()
	}
	return nil
}

func ParseHistoryQuery(since, limit string) (time.Time, int, error) {
	t := time.Now().UTC().Add(-time.Hour)
	n := 360
	if since != "" {
		var err error
		t, err = time.Parse(time.RFC3339, since)
		if err != nil {
			return t, n, fmt.Errorf("since must be RFC3339")
		}
	}
	if limit != "" {
		var err error
		n, err = strconv.Atoi(limit)
		if err != nil {
			return t, n, fmt.Errorf("invalid limit")
		}
	}
	if n < 2 || n > 2000 {
		return t, n, fmt.Errorf("limit must be 2..2000")
	}
	return t, n, nil
}
