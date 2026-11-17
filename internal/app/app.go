package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/RacFor4k/oc-remote/internal/config"
	"github.com/RacFor4k/oc-remote/internal/secret"
	"github.com/RacFor4k/oc-remote/internal/store"
	"github.com/RacFor4k/oc-remote/internal/tunnel"
	"github.com/RacFor4k/oc-remote/web"
)

const ocrCSP = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-src 'self'; frame-ancestors 'self'; base-uri 'none'; form-action 'none'"

type App struct {
	cfg    config.Config
	store  *store.Store
	hub    *tunnel.Hub
	key    []byte
	ver    string
	log    *slog.Logger
	vapidP string
	vapidK string
	admin  string
}

func New(cfg config.Config, version string, log *slog.Logger) (*App, error) {
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	key, err := secret.LoadOrCreateKey(cfg.DataDir)
	if err != nil {
		st.Close()
		return nil, err
	}
	a := &App{cfg: cfg, store: st, key: key, ver: version, log: log}
	a.hub = tunnel.NewHub(a.handleNotify, a.handleRegister)
	http.DefaultClient.Timeout = 15 * time.Second
	if err := a.loadVAPID(); err != nil {
		a.log.Warn("web push disabled", "error", err)
	}
	if err := a.loadAdminToken(); err != nil {
		st.Close()
		return nil, err
	}
	return a, nil
}

func (a *App) Close() error {
	a.hub.CloseAll()
	return a.store.Close()
}

func (a *App) Serve(ctx context.Context) error {
	pub := &http.Server{
		Addr:              a.cfg.Listen,
		Handler:           a.publicHandler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	adm := &http.Server{
		Addr:              a.cfg.AdminListen,
		Handler:           a.adminHandler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 2)
	go func() {
		a.log.Info("relay listening", "addr", a.cfg.Listen, "public", a.cfg.PublicURL)
		if err := pub.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()
	go func() {
		if err := adm.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()
	select {
	case <-ctx.Done():
		shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = pub.Shutdown(shut)
		_ = adm.Shutdown(shut)
		return nil
	case err := <-errCh:
		return err
	}
}

func (a *App) publicHandler() http.Handler {
	return a.logging(http.HandlerFunc(a.route))
}

func (a *App) route(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	switch {
	case p == "/_ocr":
		http.Redirect(w, r, "/_ocr/", http.StatusMovedPermanently)
	case p == "/_ocr/healthz":
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": a.ver})
	case strings.HasPrefix(p, "/_ocr/pair/"):
		a.handlePair(w, r)
	case p == "/_ocr/tunnel":
		a.handleTunnel(w, r)
	case p == "/_ocr/panel.css" || p == "/_ocr/panel.js" || p == "/_ocr/inject.js" || p == "/_ocr/sw.js" || p == "/_ocr/notify.js":
		a.static(w, r, strings.TrimPrefix(p, "/_ocr/"))
	case p == "/_ocr/":
		a.panel(w, r)
	case p == "/_ocr/notify":
		a.notify(w, r)
	case strings.HasPrefix(p, "/_ocr/api/"):
		a.api(w, r)
	case strings.HasPrefix(p, "/_ocr/"):
		http.NotFound(w, r)
	default:
		a.proxy(w, r)
	}
}

func (a *App) static(w http.ResponseWriter, r *http.Request, name string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	b, err := web.Read(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ct := "application/octet-stream"
	switch filepath.Ext(name) {
	case ".css":
		ct = "text/css; charset=utf-8"
	case ".js":
		ct = "text/javascript; charset=utf-8"
	case ".html":
		ct = "text/html; charset=utf-8"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if name == "sw.js" {
		w.Header().Set("Service-Worker-Allowed", "/_ocr/")
	}
	_, _ = w.Write(b)
}

func (a *App) panel(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.requireSession(w, r); !ok {
		return
	}
	b, err := web.Read("panel.html")
	if err != nil {
		http.Error(w, "panel missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", ocrCSP)
	_, _ = w.Write(b)
}

func (a *App) notify(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.requireSession(w, r); !ok {
		return
	}
	b, err := web.Read("notify.html")
	if err != nil {
		http.Error(w, "notify page missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", ocrCSP)
	_, _ = w.Write(b)
}

func (a *App) session(r *http.Request) (*store.Session, error) {
	c, err := r.Cookie(a.cfg.CookieName())
	if err != nil {
		return nil, store.ErrNotFound
	}
	h := secret.Hash([]byte(c.Value))
	sess, err := a.store.GetSession(h)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	if sess.ExpiresAt <= now {
		_ = a.store.DeleteSession(h)
		return nil, store.ErrNotFound
	}
	if now-sess.LastSeen > 60 {
		exp := now + int64(a.cfg.SessionTTL.Seconds())
		if err := a.store.TouchSession(h, now, exp); err == nil {
			sess.LastSeen = now
			sess.ExpiresAt = exp
		}
	}
	return sess, nil
}

func (a *App) requireSession(w http.ResponseWriter, r *http.Request) (*store.Session, bool) {
	sess, err := a.session(r)
	if err != nil {
		a.clearCookie(w)
		a.unauthorized(w, r)
		return nil, false
	}
	return sess, true
}

func (a *App) setCookie(w http.ResponseWriter, raw string) {
	http.SetCookie(w, &http.Cookie{
		Name:     a.cfg.CookieName(),
		Value:    raw,
		Path:     "/",
		HttpOnly: true,
		Secure:   a.cfg.SecureCookies(),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(a.cfg.SessionTTL.Seconds()),
	})
}

func (a *App) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     a.cfg.CookieName(),
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   a.cfg.SecureCookies(),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func (a *App) unauthorized(w http.ResponseWriter, r *http.Request) {
	if wantsJSON(r) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	a.errorPage(w, http.StatusUnauthorized, "Connect a device",
		`Open the pair link from <code>/remote</code> in the OpenCode TUI, or from the oc-remote panel on the laptop.`)
}

func (a *App) errorPage(w http.ResponseWriter, status int, title, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>%s</title><style>
body{margin:0;min-height:100vh;display:grid;place-items:center;background:#0f1115;color:#e6e8ee;font:15px/1.5 ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif}
main{max-width:520px;padding:28px;border:1px solid #272c36;border-radius:16px;background:#171a21}
h1{margin:0 0 10px;font-size:20px}p{margin:0 0 10px;color:#aab1c0}code{background:#0f1218;border:1px solid #272c36;border-radius:6px;padding:1px 5px}a{color:#6ee7b7}
</style></head><body><main><h1>%s</h1><p>%s</p></main></body></html>`,
		html.EscapeString(title), html.EscapeString(title), body)
}

func (a *App) offline(w http.ResponseWriter, r *http.Request) {
	if wantsJSON(r) {
		writeError(w, http.StatusBadGateway, "device offline")
		return
	}
	a.errorPage(w, http.StatusBadGateway, "Device offline",
		`The laptop is not connected. Reconnect OpenCode, then <a href="">retry</a>.`)
}

func (a *App) loadAdminToken() error {
	p := filepath.Join(a.cfg.DataDir, "admin.token")
	b, err := os.ReadFile(p)
	if err == nil {
		a.admin = strings.TrimSpace(string(b))
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	tok, err := secret.Token(32)
	if err != nil {
		return err
	}
	if err := os.WriteFile(p, []byte(tok+"\n"), 0o600); err != nil {
		return err
	}
	a.admin = tok
	return nil
}

func sameOrigin(origin, host string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, host)
}

func wantsJSON(r *http.Request) bool {
	if strings.HasPrefix(r.URL.Path, "/_ocr/api/") || strings.HasPrefix(r.URL.Path, "/api/") {
		return true
	}
	return strings.Contains(r.Header.Get("Accept"), "application/json")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("response writer does not support hijacking")
	}
	return h.Hijack()
}

func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (a *App) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		status := sw.status
		if status == 0 {
			status = http.StatusOK
		}
		a.log.Info("request",
			"method", r.Method,
			"path", safePath(r.URL.Path),
			"status", status,
			"ms", time.Since(start).Milliseconds(),
		)
	})
}

func safePath(p string) string {
	if strings.HasPrefix(p, "/_ocr/pair/") {
		return "/_ocr/pair/***"
	}
	return p
}
