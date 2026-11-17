package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RacFor4k/oc-remote/internal/config"
	"github.com/RacFor4k/oc-remote/internal/secret"
	"github.com/RacFor4k/oc-remote/internal/tunnel"
	"github.com/coder/websocket"
)

func newTestApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Config{
		PublicURL:       "http://relay.test",
		DataDir:         dir,
		SessionTTL:      time.Hour,
		MaxBody:         1 << 20,
		InsecureCookies: true,
		VAPIDSubject:    "mailto:test@example.com",
		LogLevel:        "error",
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	a, err := New(cfg, "test", log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a
}

type fakeOC struct {
	url      string
	service  string
	client   *http.Client
	ts       *httptest.Server
	mu       sync.Mutex
	bump     int
	pairSeq  int
	agentGot string
}

func newFakeOC(t *testing.T) *fakeOC {
	t.Helper()
	f := &fakeOC{service: "service-pass", client: &http.Client{}}
	mux := http.NewServeMux()

	mux.HandleFunc("/api/pair", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !f.authOK(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		f.pairSeq++
		code := fmt.Sprintf("code-%d", f.pairSeq)
		f.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"code": code, "expires_in": 300})
	})
	mux.HandleFunc("/auth/connect/", func(w http.ResponseWriter, r *http.Request) {
		code := strings.TrimPrefix(r.URL.Path, "/auth/connect/")
		if strings.Contains(r.Header.Get("Accept"), "application/json") {
			writeJSON(w, http.StatusOK, map[string]string{"token": "tok-" + code})
			return
		}
		http.Redirect(w, r, "/", http.StatusFound)
	})
	mux.HandleFunc("/api/agent", f.authed(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"data": []map[string]any{
			{"id": "build", "name": "Build", "mode": "primary", "hidden": false},
			{"id": "hidden", "name": "Hidden", "mode": "subagent", "hidden": true},
		}})
	}))
	mux.HandleFunc("/api/session/", f.authed(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/agent") || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var in struct {
			Agent string `json:"agent"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Agent == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.agentGot = in.Agent
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("/api/bump", f.authed(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.bump++
		n := f.bump
		f.mu.Unlock()
		if n == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("/api/event", f.authed(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Accel-Buffering", "no")
		fl, _ := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"type\":\"one\"}\n\n")
		if fl != nil {
			fl.Flush()
		}
		fmt.Fprint(w, "data: {\"type\":\"two\"}\n\n")
		if fl != nil {
			fl.Flush()
		}
	}))
	mux.HandleFunc("/api/echo", f.authed(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("X-Test", r.Header.Get("X-Test"))
		_, _ = w.Write(b)
	}))
	mux.HandleFunc("/", f.authed(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<!doctype html><html><head><title>ui</title></head><body>ok</body></html>")
	}))

	f.ts = httptest.NewServer(mux)
	f.url = f.ts.URL
	t.Cleanup(f.ts.Close)
	return f
}

func (f *fakeOC) authOK(r *http.Request) bool {
	u, p, ok := r.BasicAuth()
	if !ok || u != "opencode" {
		return false
	}
	return p == f.service || strings.HasPrefix(p, "tok-")
}

func (f *fakeOC) authed(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !f.authOK(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		h(w, r)
	}
}

func (f *fakeOC) mint(t *testing.T) (string, int) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, f.url+"/api/pair", nil)
	req.SetBasicAuth("opencode", f.service)
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Code      string `json:"code"`
		ExpiresIn int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.Code, out.ExpiresIn
}

type fakeDevice struct {
	ws      *websocket.Conn
	up      *fakeOC
	writeMu sync.Mutex
	mu      sync.Mutex
	cancels map[uint64]context.CancelFunc
}

func connectFakeDevice(t *testing.T, relayURL, key string, up *fakeOC) *fakeDevice {
	t.Helper()
	u, err := url.Parse(relayURL)
	if err != nil {
		t.Fatal(err)
	}
	u.Scheme = "ws"
	u.Path = "/_ocr/tunnel"
	q := u.Query()
	q.Set("key", key)
	u.RawQuery = q.Encode()
	ctx, cancel := context.WithCancel(context.Background())
	ws, _, err := websocket.Dial(ctx, u.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	d := &fakeDevice{ws: ws, up: up, cancels: map[uint64]context.CancelFunc{}}
	go d.loop(ctx)
	t.Cleanup(func() {
		cancel()
		d.stop()
	})
	return d
}

func (d *fakeDevice) stop() {
	d.mu.Lock()
	for _, c := range d.cancels {
		c()
	}
	d.cancels = map[uint64]context.CancelFunc{}
	d.mu.Unlock()
	_ = d.ws.Close(websocket.StatusNormalClosure, "bye")
}

func (d *fakeDevice) send(t *testing.T, f tunnel.Frame) {
	t.Helper()
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := d.ws.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatal(err)
	}
}

func (d *fakeDevice) loop(ctx context.Context) {
	for {
		typ, data, err := d.ws.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		var f tunnel.Frame
		if json.Unmarshal(data, &f) != nil {
			continue
		}
		switch f.T {
		case "req":
			d.mu.Lock()
			reqCtx, cancel := context.WithCancel(context.Background())
			d.cancels[f.ID] = cancel
			d.mu.Unlock()
			go d.handleReq(reqCtx, f)
		case "pair.mint":
			go d.handleMint(f)
		case "abort":
			d.mu.Lock()
			if c := d.cancels[f.ID]; c != nil {
				c()
			}
			d.mu.Unlock()
		}
	}
}

func (d *fakeDevice) handleReq(ctx context.Context, f tunnel.Frame) {
	defer func() {
		d.mu.Lock()
		delete(d.cancels, f.ID)
		d.mu.Unlock()
	}()
	var body io.Reader
	if len(f.Body) > 0 {
		body = bytes.NewReader(f.Body)
	}
	req, err := http.NewRequestWithContext(ctx, f.Method, d.up.url+f.Path, body)
	if err != nil {
		d.fail(f.ID, err)
		return
	}
	for _, h := range f.Headers {
		req.Header.Add(h[0], h[1])
	}
	if f.Auth != "" {
		req.Header.Set("Authorization", f.Auth)
	}
	resp, err := d.up.client.Do(req)
	if err != nil {
		d.fail(f.ID, err)
		return
	}
	defer resp.Body.Close()
	headers := make([][2]string, 0, len(resp.Header))
	for k, vs := range resp.Header {
		for _, v := range vs {
			headers = append(headers, [2]string{k, v})
		}
	}
	if err := d.rawSend(tunnel.Frame{T: "res.start", ID: f.ID, Status: resp.StatusCode, Headers: headers}); err != nil {
		return
	}
	buf := make([]byte, 4096)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			if err := d.rawSend(tunnel.Frame{T: "res.chunk", ID: f.ID, Body: chunk}); err != nil {
				return
			}
		}
		if rerr != nil {
			break
		}
	}
	_ = d.rawSend(tunnel.Frame{T: "res.end", ID: f.ID})
}

func (d *fakeDevice) handleMint(f tunnel.Frame) {
	code := fmt.Sprintf("mint-%d", f.ID)
	if err := d.rawSend(tunnel.Frame{T: "pair.minted", ID: f.ID, Code: code, ExpiresIn: 300}); err != nil {
		return
	}
}

func (d *fakeDevice) fail(id uint64, err error) {
	_ = d.rawSend(tunnel.Frame{T: "res.error", ID: id, Error: err.Error()})
}

func (d *fakeDevice) rawSend(f tunnel.Frame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return d.ws.Write(ctx, websocket.MessageText, b)
}

func waitOnline(t *testing.T, a *App, deviceID string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if a.hub.Online(deviceID) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("device did not connect")
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRelayFlow(t *testing.T) {
	a := newTestApp(t)
	up := newFakeOC(t)
	relay := httptest.NewServer(a.publicHandler())
	t.Cleanup(relay.Close)
	a.cfg.PublicURL = relay.URL

	ten, err := a.store.CreateTenant("alice")
	if err != nil {
		t.Fatal(err)
	}
	key, err := secret.Token(32)
	if err != nil {
		t.Fatal(err)
	}
	dev, err := a.store.CreateDevice(ten.ID, "alice-laptop", secret.Hash([]byte(key)))
	if err != nil {
		t.Fatal(err)
	}
	d := connectFakeDevice(t, relay.URL, key, up)
	waitOnline(t, a, dev.ID)

	code, _ := up.mint(t)
	d.send(t, tunnel.Frame{T: "pair.register", Code: code, ExpiresIn: 300})

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	var resp *http.Response
	for i := 0; i < 100; i++ {
		resp, err = client.Get(relay.URL + "/_ocr/pair/" + code)
		if err == nil && resp.StatusCode != http.StatusNotFound {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pair: status %d", resp.StatusCode)
	}
	if !strings.Contains(body, "/_ocr/inject.js") {
		t.Fatalf("html was not injected: %s", body)
	}
	if resp.Header.Get("Content-Length") == "" {
		t.Fatal("injected html has no content-length")
	}

	u, _ := url.Parse(relay.URL)
	cookies := jar.Cookies(u)
	if len(cookies) != 1 || cookies[0].Name != "ocr_session" {
		t.Fatalf("unexpected cookies: %#v", cookies)
	}

	resp, err = client.Get(relay.URL + "/_ocr/")
	if err != nil {
		t.Fatal(err)
	}
	body = readAll(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "oc-remote") {
		t.Fatalf("panel: %d %s", resp.StatusCode, body)
	}
	if resp.Header.Get("Content-Security-Policy") == "" {
		t.Fatal("panel has no CSP")
	}

	resp, err = client.Get(relay.URL + "/_ocr/notify")
	if err != nil {
		t.Fatal(err)
	}
	body = readAll(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "/_ocr/notify.js") {
		t.Fatalf("notify page: %d %s", resp.StatusCode, body)
	}

	resp, err = client.Get(relay.URL + "/_ocr/api/push/key")
	if err != nil {
		t.Fatal(err)
	}
	body = readAll(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"key"`) {
		t.Fatalf("push key: %d %s", resp.StatusCode, body)
	}

	sub, _ := json.Marshal(map[string]any{
		"endpoint": "https://example.com/push/1",
		"keys":     map[string]string{"p256dh": "p", "auth": "a"},
	})
	preq, _ := http.NewRequest(http.MethodPost, relay.URL+"/_ocr/api/push/subscribe", bytes.NewReader(sub))
	preq.Header.Set("Content-Type", "application/json")
	preq.Header.Set("X-OCR-CSRF", "1")
	preq.Header.Set("Origin", relay.URL)
	resp, err = client.Do(preq)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("push subscribe: %d", resp.StatusCode)
	}
	resp, err = client.Get(relay.URL + "/_ocr/api/status")
	if err != nil {
		t.Fatal(err)
	}
	body = readAll(t, resp)
	if !strings.Contains(body, `"push":true`) {
		t.Fatalf("status push: %s", body)
	}

	resp, err = client.Get(relay.URL + "/_ocr/api/agents")
	if err != nil {
		t.Fatal(err)
	}
	body = readAll(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"build"`) {
		t.Fatalf("agents: %d %s", resp.StatusCode, body)
	}
	if strings.Contains(body, `"hidden"`) {
		t.Fatalf("hidden agent leaked: %s", body)
	}

	req, _ := http.NewRequest(http.MethodPost, relay.URL+"/_ocr/api/agent",
		strings.NewReader(`{"sessionID":"ses_test","agent":"plan"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-OCR-CSRF", "1")
	req.Header.Set("Origin", relay.URL)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("agent switch: status %d", resp.StatusCode)
	}
	up.mu.Lock()
	got := up.agentGot
	up.mu.Unlock()
	if got != "plan" {
		t.Fatalf("agent switch body: %q", got)
	}

	resp, err = client.Get(relay.URL + "/api/bump")
	if err != nil {
		t.Fatal(err)
	}
	body = readAll(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"ok"`) {
		t.Fatalf("renew: %d %s", resp.StatusCode, body)
	}
	up.mu.Lock()
	bump := up.bump
	up.mu.Unlock()
	if bump != 2 {
		t.Fatalf("expected one unauthorized retry, bump=%d", bump)
	}

	resp, err = client.Get(relay.URL + "/api/event")
	if err != nil {
		t.Fatal(err)
	}
	body = readAll(t, resp)
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("sse content type: %s", resp.Header.Get("Content-Type"))
	}
	if !strings.Contains(body, `"one"`) || !strings.Contains(body, `"two"`) {
		t.Fatalf("sse body: %s", body)
	}

	req, _ = http.NewRequest(http.MethodPost, relay.URL+"/_ocr/api/logout-all", nil)
	req.Header.Set("X-OCR-CSRF", "1")
	req.Header.Set("Origin", relay.URL)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout: status %d", resp.StatusCode)
	}
	resp, err = client.Get(relay.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("after logout: status %d", resp.StatusCode)
	}
}

func TestPairSingleUse(t *testing.T) {
	a := newTestApp(t)
	up := newFakeOC(t)
	relay := httptest.NewServer(a.publicHandler())
	t.Cleanup(relay.Close)
	a.cfg.PublicURL = relay.URL

	ten, _ := a.store.CreateTenant("bob")
	key, _ := secret.Token(32)
	dev, _ := a.store.CreateDevice(ten.ID, "bob-laptop", secret.Hash([]byte(key)))
	d := connectFakeDevice(t, relay.URL, key, up)
	waitOnline(t, a, dev.ID)

	code, _ := up.mint(t)
	d.send(t, tunnel.Frame{T: "pair.register", Code: code, ExpiresIn: 300})

	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	var resp *http.Response
	var err error
	for i := 0; i < 100; i++ {
		resp, err = noFollow.Get(relay.URL + "/_ocr/pair/" + code)
		if err == nil && resp.StatusCode != http.StatusNotFound {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("pair: status %d", resp.StatusCode)
	}
	sc := resp.Header.Values("Set-Cookie")
	_ = resp.Body.Close()
	if len(sc) == 0 {
		t.Fatal("no set-cookie")
	}
	joined := strings.Join(sc, ";")
	for _, want := range []string{"ocr_session=", "HttpOnly", "SameSite=Lax", "Path=/"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("cookie missing %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, "Secure") {
		t.Fatalf("insecure mode should not set Secure: %s", joined)
	}

	resp, err = noFollow.Get(relay.URL + "/_ocr/pair/" + code)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("second use: status %d", resp.StatusCode)
	}
}

func TestAdminFlow(t *testing.T) {
	a := newTestApp(t)
	srv := httptest.NewServer(a.adminHandler())
	t.Cleanup(srv.Close)

	call := func(method, path string, in any, out any) int {
		var r io.Reader
		if in != nil {
			b, _ := json.Marshal(in)
			r = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, srv.URL+path, r)
		req.Header.Set("Authorization", "Bearer "+a.admin)
		if in != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if out != nil {
			_ = json.NewDecoder(resp.Body).Decode(out)
		} else {
			_, _ = io.Copy(io.Discard, resp.Body)
		}
		return resp.StatusCode
	}

	var added struct {
		Tenant string `json:"tenant"`
		Device string `json:"device"`
		Key    string `json:"key"`
		Join   string `json:"join"`
	}
	if st := call(http.MethodPost, "/admin/tenant/add", map[string]string{"name": "carol"}, &added); st != http.StatusOK {
		t.Fatalf("add: %d", st)
	}
	if added.Tenant != "carol" || !strings.HasPrefix(added.Key, "ocr_") {
		t.Fatalf("add response: %#v", added)
	}
	if _, _, err := a.store.GetDeviceByKeyHash(secret.Hash([]byte(added.Key))); err != nil {
		t.Fatalf("device key not stored: %v", err)
	}

	var list struct {
		Tenants []struct {
			Name     string `json:"name"`
			Disabled bool   `json:"disabled"`
			Devices  []struct {
				Name string `json:"name"`
			} `json:"devices"`
		} `json:"tenants"`
	}
	if st := call(http.MethodGet, "/admin/list", nil, &list); st != http.StatusOK {
		t.Fatalf("list: %d", st)
	}
	if len(list.Tenants) != 1 || list.Tenants[0].Name != "carol" {
		t.Fatalf("list: %#v", list)
	}

	if st := call(http.MethodPost, "/admin/tenant/disable", map[string]string{"name": "carol"}, nil); st != http.StatusNoContent {
		t.Fatalf("disable: %d", st)
	}
	if st := call(http.MethodPost, "/admin/tenant/remove", map[string]string{"name": "carol"}, nil); st != http.StatusNoContent {
		t.Fatalf("remove: %d", st)
	}
	if st := call(http.MethodGet, "/admin/list", nil, &list); st != http.StatusOK {
		t.Fatalf("list: %d", st)
	}
	if len(list.Tenants) != 0 {
		t.Fatalf("tenant not removed: %#v", list)
	}
}

func TestWrongTunnelKey(t *testing.T) {
	a := newTestApp(t)
	relay := httptest.NewServer(a.publicHandler())
	t.Cleanup(relay.Close)

	u, _ := url.Parse(relay.URL)
	u.Scheme = "ws"
	u.Path = "/_ocr/tunnel"
	q := u.Query()
	q.Set("key", "nope")
	u.RawQuery = q.Encode()
	_, resp, err := websocket.Dial(context.Background(), u.String(), nil)
	if err == nil {
		t.Fatal("expected dial failure")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %#v", resp)
	}
}
