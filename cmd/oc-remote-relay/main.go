package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/RacFor4k/oc-remote/internal/app"
	"github.com/RacFor4k/oc-remote/internal/config"
)

var version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "serve":
		runServe()
	case "admin":
		runAdmin(os.Args[2:])
	case "healthcheck":
		runHealth()
	case "version":
		fmt.Println(version)
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  oc-remote-relay serve
  oc-remote-relay admin add <tenant> [--device <name>]
  oc-remote-relay admin rm <tenant>
  oc-remote-relay admin disable <tenant>
  oc-remote-relay admin enable <tenant>
  oc-remote-relay admin rotate-key <tenant> [--device <name>]
  oc-remote-relay admin list
  oc-remote-relay healthcheck
  oc-remote-relay version`)
}

func runServe() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: parseLevel(cfg.LogLevel)}))
	a, err := app.New(cfg, version, log)
	if err != nil {
		log.Error("start failed", "error", err)
		os.Exit(1)
	}
	defer a.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := a.Serve(ctx); err != nil {
		log.Error("relay stopped", "error", err)
		os.Exit(1)
	}
}

func runHealth() {
	u := env("OC_REMOTE_HEALTH_URL", "http://127.0.0.1:4097/_ocr/healthz")
	c := &http.Client{Timeout: 5 * time.Second}
	resp, err := c.Get(u)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "health: status %d\n", resp.StatusCode)
		os.Exit(1)
	}
	fmt.Println("ok")
}

func runAdmin(args []string) {
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	base := strings.TrimRight(env("OC_REMOTE_ADMIN_URL", "http://127.0.0.1:4098"), "/")
	token, err := adminToken()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	client := &http.Client{Timeout: 20 * time.Second}
	call := func(method, path string, in, out any) error {
		var body io.Reader
		if in != nil {
			b, err := json.Marshal(in)
			if err != nil {
				return err
			}
			body = bytes.NewReader(b)
		}
		req, err := http.NewRequest(method, base+path, body)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if in != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if resp.StatusCode >= 300 {
			return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
		}
		if out != nil && len(b) > 0 {
			return json.Unmarshal(b, out)
		}
		return nil
	}

	switch args[0] {
	case "add":
		name, device, err := parseNameDevice(args[1:])
		if err != nil {
			fail(err)
		}
		var out struct {
			Tenant string `json:"tenant"`
			Device string `json:"device"`
			Key    string `json:"key"`
			Join   string `json:"join"`
		}
		if err := call(http.MethodPost, "/admin/tenant/add", map[string]string{"name": name, "device": device}, &out); err != nil {
			fail(err)
		}
		printAccount(out.Tenant, out.Device, out.Key, out.Join)
	case "rm":
		name, _, err := parseNameDevice(args[1:])
		if err != nil {
			fail(err)
		}
		if err := call(http.MethodPost, "/admin/tenant/remove", map[string]string{"name": name}, nil); err != nil {
			fail(err)
		}
		fmt.Println("removed", name)
	case "disable":
		name, _, err := parseNameDevice(args[1:])
		if err != nil {
			fail(err)
		}
		if err := call(http.MethodPost, "/admin/tenant/disable", map[string]string{"name": name}, nil); err != nil {
			fail(err)
		}
		fmt.Println("disabled", name)
	case "enable":
		name, _, err := parseNameDevice(args[1:])
		if err != nil {
			fail(err)
		}
		if err := call(http.MethodPost, "/admin/tenant/enable", map[string]string{"name": name}, nil); err != nil {
			fail(err)
		}
		fmt.Println("enabled", name)
	case "rotate-key":
		name, device, err := parseNameDevice(args[1:])
		if err != nil {
			fail(err)
		}
		var out struct {
			Tenant string `json:"tenant"`
			Device string `json:"device"`
			Key    string `json:"key"`
			Join   string `json:"join"`
		}
		if err := call(http.MethodPost, "/admin/device/rotate-key", map[string]string{"tenant": name, "device": device}, &out); err != nil {
			fail(err)
		}
		printAccount(out.Tenant, out.Device, out.Key, out.Join)
	case "list":
		var out struct {
			Tenants []struct {
				Name     string `json:"name"`
				Disabled bool   `json:"disabled"`
				Sessions int    `json:"sessions"`
				Push     int    `json:"push"`
				Devices  []struct {
					Name     string `json:"name"`
					Disabled bool   `json:"disabled"`
					Online   bool   `json:"online"`
					LastSeen *int64 `json:"lastSeen"`
				} `json:"devices"`
			} `json:"tenants"`
		}
		if err := call(http.MethodGet, "/admin/list", nil, &out); err != nil {
			fail(err)
		}
		fmt.Printf("%-16s %-20s %-6s %-4s %-4s %-8s %s\n", "TENANT", "DEVICE", "ONLINE", "SESS", "PUSH", "DISABLED", "LAST SEEN")
		for _, t := range out.Tenants {
			if len(t.Devices) == 0 {
				fmt.Printf("%-16s %-20s %-6s %-4d %-4d %-8s %s\n", t.Name, "-", "no", t.Sessions, t.Push, disabledText(t.Disabled), "-")
				continue
			}
			for _, d := range t.Devices {
				online := "no"
				if d.Online {
					online = "yes"
				}
				fmt.Printf("%-16s %-20s %-6s %-4d %-4d %-8s %s\n",
					t.Name, d.Name, online, t.Sessions, t.Push,
					disabledText(t.Disabled || d.Disabled), ago(d.LastSeen))
			}
		}
	default:
		usage()
		os.Exit(2)
	}
}

func adminToken() (string, error) {
	if v := os.Getenv("OC_REMOTE_ADMIN_TOKEN"); v != "" {
		return v, nil
	}
	dir := env("OC_REMOTE_DATA_DIR", "/data")
	b, err := os.ReadFile(filepath.Join(dir, "admin.token"))
	if err != nil {
		return "", fmt.Errorf("admin token: %w (is the relay running and OC_REMOTE_DATA_DIR correct?)", err)
	}
	return strings.TrimSpace(string(b)), nil
}

func parseNameDevice(args []string) (string, string, error) {
	var name, device string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--device" || a == "-d":
			if i+1 >= len(args) {
				return "", "", errors.New("--device needs a value")
			}
			device = args[i+1]
			i++
		case strings.HasPrefix(a, "--device="):
			device = strings.TrimPrefix(a, "--device=")
		case strings.HasPrefix(a, "-"):
			return "", "", fmt.Errorf("unknown flag %s", a)
		case name == "":
			name = a
		default:
			return "", "", fmt.Errorf("unexpected argument %s", a)
		}
	}
	if name == "" {
		return "", "", errors.New("tenant name is required")
	}
	return name, device, nil
}

func printAccount(tenant, device, key, join string) {
	fmt.Printf("tenant: %s\ndevice: %s\nkey:    %s\njoin:   %s\n", tenant, device, key, join)
}

func disabledText(v bool) string {
	if v {
		return "yes"
	}
	return ""
}

func ago(ts *int64) string {
	if ts == nil {
		return "-"
	}
	d := time.Since(time.Unix(*ts, 0))
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug", "trace", "all":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error", "fatal":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
