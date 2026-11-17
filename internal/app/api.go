package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/RacFor4k/oc-remote/internal/secret"
	"github.com/RacFor4k/oc-remote/internal/store"
	"github.com/RacFor4k/oc-remote/internal/tunnel"
)

func (a *App) api(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/_ocr/api/")
	if p == "verify" {
		a.apiVerify(w, r)
		return
	}
	sess, ok := a.requireSession(w, r)
	if !ok {
		return
	}
	if r.Method == http.MethodPost {
		if r.Header.Get("X-OCR-CSRF") != "1" {
			writeError(w, http.StatusForbidden, "missing csrf header")
			return
		}
		if o := r.Header.Get("Origin"); o != "" && !sameOrigin(o, r.Host) {
			writeError(w, http.StatusForbidden, "bad origin")
			return
		}
	}

	switch p {
	case "status":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		a.apiStatus(w, r, sess)
	case "pair":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		a.apiPair(w, r, sess)
	case "logout-all":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		a.apiLogoutAll(w, r, sess)
	case "agents":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		a.apiAgents(w, r, sess)
	case "agent":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		a.apiAgent(w, r, sess)
	case "push/key":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		a.apiPushKey(w, r)
	case "push/subscribe":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		a.apiPushSubscribe(w, r, sess)
	case "push/unsubscribe":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		a.apiPushUnsubscribe(w, r, sess)
	default:
		http.NotFound(w, r)
	}
}

func (a *App) apiStatus(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	dev, err := a.store.GetDevice(sess.DeviceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "device missing")
		return
	}
	ten, err := a.store.GetTenant(sess.TenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "tenant missing")
		return
	}
	sessions, _ := a.store.CountSessions(sess.TenantID)
	push, _ := a.store.CountPushSubs(sess.TenantID)
	writeJSON(w, http.StatusOK, map[string]any{
		"tenant":   ten.Name,
		"device":   dev.Name,
		"online":   a.hub.Online(dev.ID),
		"disabled": dev.Disabled || ten.Disabled,
		"sessions": sessions,
		"push":     push > 0,
		"lastSeen": dev.LastSeen,
		"version":  a.ver,
	})
}

func (a *App) apiPair(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	u, exp, err := a.mintPair(ctx, sess)
	if errors.Is(err, errOffline) {
		writeError(w, http.StatusServiceUnavailable, "device offline")
		return
	}
	if err != nil {
		a.log.Warn("pair mint failed", "device", sess.DeviceID, "error", err)
		writeError(w, http.StatusBadGateway, "pair mint failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": u, "expiresAt": exp})
}

func (a *App) apiLogoutAll(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if _, err := a.store.DeleteTenantSessions(sess.TenantID); err != nil {
		writeError(w, http.StatusInternalServerError, "could not delete sessions")
		return
	}
	a.clearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) apiAgents(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	status, body, err := a.upstreamJSON(ctx, sess, http.MethodGet, "/api/agent", nil, 4<<20)
	if errors.Is(err, errOffline) {
		writeError(w, http.StatusServiceUnavailable, "device offline")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, "agent list failed")
		return
	}
	if status != http.StatusOK {
		writeError(w, http.StatusBadGateway, "agent list failed")
		return
	}
	var in struct {
		Data []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description"`
			Mode        string `json:"mode"`
			Hidden      bool   `json:"hidden"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		writeError(w, http.StatusBadGateway, "agent list malformed")
		return
	}
	out := make([]map[string]any, 0, len(in.Data))
	for _, ag := range in.Data {
		if ag.Hidden {
			continue
		}
		out = append(out, map[string]any{
			"id":          ag.ID,
			"name":        ag.Name,
			"description": ag.Description,
			"mode":        ag.Mode,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": out})
}

func (a *App) apiAgent(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	var in struct {
		SessionID string `json:"sessionID"`
		Agent     string `json:"agent"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "bad json")
		return
	}
	if in.SessionID == "" || in.Agent == "" || strings.Contains(in.SessionID, "/") {
		writeError(w, http.StatusBadRequest, "sessionID and agent are required")
		return
	}
	payload, _ := json.Marshal(map[string]string{"agent": in.Agent})
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	status, _, err := a.upstreamJSON(ctx, sess, http.MethodPost,
		"/api/session/"+url.PathEscape(in.SessionID)+"/agent", payload, 1<<20)
	if errors.Is(err, errOffline) {
		writeError(w, http.StatusServiceUnavailable, "device offline")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, "agent switch failed")
		return
	}
	if status == http.StatusNoContent {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeError(w, http.StatusBadGateway, "agent switch failed")
}

func (a *App) apiPushKey(w http.ResponseWriter, r *http.Request) {
	if a.vapidK == "" {
		writeError(w, http.StatusServiceUnavailable, "push disabled")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"key": a.vapidK})
}

func (a *App) apiPushSubscribe(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	var in struct {
		Endpoint string `json:"endpoint"`
		Keys     struct {
			P256dh string `json:"p256dh"`
			Auth   string `json:"auth"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "bad json")
		return
	}
	if !strings.HasPrefix(in.Endpoint, "https://") || in.Keys.P256dh == "" || in.Keys.Auth == "" {
		writeError(w, http.StatusBadRequest, "invalid subscription")
		return
	}
	if err := a.store.AddPushSub(store.PushSub{
		TenantID: sess.TenantID,
		Endpoint: in.Endpoint,
		P256dh:   in.Keys.P256dh,
		Auth:     in.Keys.Auth,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "could not store subscription")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) apiPushUnsubscribe(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	var in struct {
		Endpoint string `json:"endpoint"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "bad json")
		return
	}
	if in.Endpoint == "" {
		writeError(w, http.StatusBadRequest, "endpoint is required")
		return
	}
	_ = a.store.DeletePushSub(in.Endpoint)
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) apiVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		writeError(w, http.StatusUnauthorized, "missing key")
		return
	}
	key := strings.TrimPrefix(h, "Bearer ")
	dev, ten, err := a.store.GetDeviceByKeyHash(secret.Hash([]byte(key)))
	if err != nil || dev.Disabled || ten.Disabled {
		writeError(w, http.StatusForbidden, "invalid key")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"tenant": ten.Name, "device": dev.Name})
}

func (a *App) upstreamJSON(ctx context.Context, sess *store.Session, method, path string, body []byte, limit int64) (int, []byte, error) {
	for attempt := 0; attempt < 2; attempt++ {
		conn := a.hub.Get(sess.DeviceID)
		if conn == nil {
			return 0, nil, errOffline
		}
		token, err := secret.Decrypt(a.key, sess.Token)
		if err != nil {
			return 0, nil, err
		}
		headers := [][2]string{{"accept", "application/json"}}
		if len(body) > 0 {
			headers = append(headers, [2]string{"content-type", "application/json"})
		}
		var status int
		var buf bytes.Buffer
		err = conn.Do(ctx,
			tunnel.Frame{
				T:       "req",
				Method:  method,
				Path:    path,
				Headers: headers,
				Auth:    basicAuth(string(token)),
				Body:    body,
			},
			func(f tunnel.Frame) error {
				status = f.Status
				return nil
			},
			func(b []byte) error {
				if limit > 0 && int64(buf.Len()+len(b)) > limit {
					return errBodyTooLarge
				}
				_, _ = buf.Write(b)
				return nil
			})
		if err != nil {
			return status, nil, err
		}
		if status == http.StatusUnauthorized && attempt == 0 {
			if err := a.renew(ctx, sess); err != nil {
				return status, nil, errUnauthorized
			}
			continue
		}
		return status, buf.Bytes(), nil
	}
	return http.StatusUnauthorized, nil, errUnauthorized
}
