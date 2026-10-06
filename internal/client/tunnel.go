package client

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os/exec"
	"sync"
	"time"
)

type tunnel struct {
	mu          sync.Mutex
	endpoint    string
	done        chan error
	lastAttempt time.Time
}

func (t *tunnel) Endpoint(ctx context.Context, n Node) (string, error) {
	if n.SSH == "" {
		return n.URL, nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done != nil {
		select {
		case <-t.done:
			t.done = nil
			t.endpoint = ""
		default:
			return t.endpoint, nil
		}
	}
	if time.Since(t.lastAttempt) < 5*time.Second {
		return "", fmt.Errorf("SSH tunnel reconnecting")
	}
	t.lastAttempt = time.Now()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	u, _ := url.Parse(n.URL)
	forward := fmt.Sprintf("127.0.0.1:%d:%s", port, net.JoinHostPort(u.Hostname(), u.Port()))
	cmd := exec.CommandContext(ctx, "ssh", "-N", "-T", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ExitOnForwardFailure=yes", "-o", "ConnectTimeout=5", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3", "-L", forward, "--", n.SSH)
	hideWindow(cmd)
	if err = cmd.Start(); err != nil {
		return "", fmt.Errorf("start SSH: %w", err)
	}
	done := make(chan error, 1)
	t.done = done
	go func() { done <- cmd.Wait() }()
	address := fmt.Sprintf("127.0.0.1:%d", port)
	deadline := time.NewTimer(4 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-deadline.C:
			cmd.Process.Kill()
			<-done
			t.done = nil
			return "", fmt.Errorf("SSH tunnel timed out")
		case <-done:
			t.done = nil
			return "", fmt.Errorf("SSH tunnel failed; verify ssh %s and the remote agent", n.SSH)
		case <-tick.C:
			connection, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
			if err == nil {
				connection.Close()
				t.endpoint = "http://" + address
				return t.endpoint, nil
			}
		}
	}
}
