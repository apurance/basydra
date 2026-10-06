package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"basydra/internal/agent"
	"basydra/internal/client"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		args = []string{"serve"}
	}
	switch args[0] {
	case "version", "--version":
		fmt.Println("Basydra 0.1.0")
		return nil
	case "help", "--help", "-h":
		fmt.Println("Basydra — one client, many nodes.\n\n  basydra serve [--listen 127.0.0.1:17890] [--config FILE]\n  basydra agent --token-file FILE [--listen 127.0.0.1:19790]\n  basydra node add --name NAME --url URL --token-file FILE [--ssh HOST]\n  basydra node list\n  basydra node remove --id ID\n  basydra status\n\nClient config defaults to basydra/client.json in the OS user configuration directory.\nNode commands use the running client at http://127.0.0.1:17890 (--client URL).\nAgents require Linux. Client binaries include the entire web UI.")
		return nil
	case "serve":
		f := flag.NewFlagSet("serve", flag.ContinueOnError)
		listen := f.String("listen", "127.0.0.1:17890", "local UI address")
		config := f.String("config", "", "node configuration file (default: OS user configuration directory/basydra/client.json)")
		interval := f.Duration("interval", 5*time.Second, "poll interval")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if *config == "" {
			path, err := client.DefaultConfigPath()
			if err != nil {
				return err
			}
			*config = path
		}
		return client.Run(ctx, *listen, *config, *interval)
	case "agent":
		f := flag.NewFlagSet("agent", flag.ContinueOnError)
		opts := agent.Options{}
		f.StringVar(&opts.Listen, "listen", "127.0.0.1:19790", "probe address")
		f.StringVar(&opts.DataDir, "data-dir", "basydra-data", "node-local history directory")
		f.DurationVar(&opts.Interval, "interval", 5*time.Second, "sample interval")
		f.DurationVar(&opts.Retention, "retention", 24*time.Hour, "local retention (1h..72h)")
		f.StringVar(&opts.TLSCert, "tls-cert", "", "TLS certificate PEM")
		f.StringVar(&opts.TLSKey, "tls-key", "", "TLS key PEM")
		tokenFile := f.String("token-file", "", "read token from file (or BASYDRA_TOKEN environment)")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		token, err := readToken(*tokenFile)
		if err != nil {
			return err
		}
		opts.Token = token
		return agent.Run(ctx, opts)
	case "status":
		return nodeCommand(ctx, []string{"list"}, args[1:])
	case "node":
		if len(args) < 2 {
			return fmt.Errorf("use node add, list or remove")
		}
		return nodeCommand(ctx, args[1:2], args[2:])
	default:
		return fmt.Errorf("unknown command %q; run basydra help", args[0])
	}
}

func readToken(path string) (string, error) {
	if path == "" {
		return strings.TrimSpace(os.Getenv("BASYDRA_TOKEN")), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func nodeCommand(ctx context.Context, command, args []string) error {
	f := flag.NewFlagSet("node "+command[0], flag.ContinueOnError)
	server := f.String("client", "http://127.0.0.1:17890", "running client URL")
	n := client.Node{}
	f.StringVar(&n.Name, "name", "", "display name")
	f.StringVar(&n.URL, "url", "http://127.0.0.1:19790", "agent URL (remote loopback for SSH)")
	f.StringVar(&n.SSH, "ssh", "", "SSH target alias")
	tokenFile := f.String("token-file", "", "token file or BASYDRA_TOKEN")
	id := f.String("id", "", "node ID")
	if err := f.Parse(args); err != nil {
		return err
	}
	method, path := "GET", "/api/nodes"
	var body io.Reader
	switch command[0] {
	case "list":
	case "add":
		token, err := readToken(*tokenFile)
		if err != nil {
			return err
		}
		n.Token = token
		if err = n.Validate(); err != nil {
			return err
		}
		data, _ := json.Marshal(n)
		body = bytes.NewReader(data)
		method = "POST"
	case "remove":
		if len(*id) != 16 {
			return fmt.Errorf("--id is required")
		}
		method = "DELETE"
		path += "/" + *id
	default:
		return fmt.Errorf("unknown node command")
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(*server, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	httpClient := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach client; start basydra serve first: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if len(data) > 0 {
		var pretty bytes.Buffer
		if json.Indent(&pretty, data, "", "  ") == nil {
			fmt.Println(pretty.String())
		} else {
			fmt.Println(string(data))
		}
	}
	return nil
}
