package client

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type Node struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	URL   string `json:"url"`
	SSH   string `json:"ssh,omitempty"`
	Token string `json:"token,omitempty"`
}

type Config struct {
	Version int    `json:"version"`
	Nodes   []Node `json:"nodes"`
}

var sshTarget = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.@:-]{0,199}$`)
var nodeID = regexp.MustCompile(`^[a-f0-9]{16}$`)

func DefaultConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user configuration directory: %w", err)
	}
	return filepath.Join(dir, "basydra", "client.json"), nil
}

func (n *Node) Validate() error {
	n.Name = strings.TrimSpace(n.Name)
	if n.Name == "" || len(n.Name) > 80 {
		return fmt.Errorf("name must contain 1..80 bytes")
	}
	if len(n.Token) < 24 || len(n.Token) > 4096 || strings.ContainsAny(n.Token, "\r\n") {
		return fmt.Errorf("token must contain 24..4096 characters and no newlines")
	}
	u, err := url.Parse(n.URL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return fmt.Errorf("URL must be an http(s) origin without credentials, query or path")
	}
	ip := net.ParseIP(u.Hostname())
	loopback := ip != nil && ip.IsLoopback()
	if n.SSH != "" {
		if !sshTarget.MatchString(n.SSH) {
			return fmt.Errorf("SSH must be a host alias or user@host")
		}
		if u.Scheme != "http" || !loopback || u.Port() == "" {
			return fmt.Errorf("SSH nodes require an explicit remote loopback URL, e.g. http://127.0.0.1:19790")
		}
	} else if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return fmt.Errorf("direct remote nodes require HTTPS; HTTP is allowed on loopback only")
	}
	n.URL = strings.TrimSuffix(n.URL, "/")
	return nil
}

func LoadConfig(path string) (Config, error) {
	c := Config{Version: 1, Nodes: []Node{}}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err = json.Unmarshal(data, &c); err != nil {
		return c, err
	}
	if c.Version != 1 {
		return c, fmt.Errorf("unsupported config version %d", c.Version)
	}
	if len(c.Nodes) > 128 {
		return c, fmt.Errorf("too many nodes (max 128)")
	}
	seen := map[string]bool{}
	for i := range c.Nodes {
		n := &c.Nodes[i]
		if !nodeID.MatchString(n.ID) || seen[n.ID] {
			return c, fmt.Errorf("invalid or duplicate node ID")
		}
		seen[n.ID] = true
		if err = n.Validate(); err != nil {
			return c, fmt.Errorf("node %s: %w", n.ID, err)
		}
	}
	return c, nil
}

func saveConfig(path string, c Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".config-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err = f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func NewID() (string, error) {
	var b [8]byte
	_, err := rand.Read(b[:])
	return hex.EncodeToString(b[:]), err
}
func (n Node) Public() Node { n.Token = ""; return n }
