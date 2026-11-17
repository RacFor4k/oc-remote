package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/RacFor4k/oc-remote/internal/secret"
	"github.com/RacFor4k/oc-remote/internal/store"
	"github.com/RacFor4k/oc-remote/internal/tunnel"
)

func (a *App) handlePair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	code := strings.TrimPrefix(r.URL.Path, "/_ocr/pair/")
	if code == "" || strings.Contains(code, "/") {
		a.errorPage(w, http.StatusNotFound, "Link not valid",
			"This pair link expired, was already used, or never existed. Mint a new one from <code>/remote</code>.")
		return
	}
	deviceID, err := a.store.ConsumePairCode(secret.Hash([]byte(code)), time.Now().Unix())
	if err != nil {
		a.errorPage(w, http.StatusNotFound, "Link not valid",
			"This pair link expired, was already used, or never existed. Mint a new one from <code>/remote</code>.")
		return
	}
	conn := a.hub.Get(deviceID)
	if conn == nil {
		a.offline(w, r)
		return
	}
	dev, err := a.store.GetDevice(deviceID)
	if err != nil || dev.Disabled {
		a.errorPage(w, http.StatusForbidden, "Device disabled", "This device was disabled by the administrator.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	token, err := a.pairRedeem(ctx, conn, code)
	if err != nil {
		a.log.Warn("pair redeem failed", "device", dev.Name, "error", err)
		a.errorPage(w, http.StatusBadGateway, "Pairing failed",
			"The device did not accept the link. Mint a new one from <code>/remote</code>.")
		return
	}
	raw, err := secret.Token(32)
	if err != nil {
		a.errorPage(w, http.StatusInternalServerError, "Internal error", "Could not create a session.")
		return
	}
	enc, err := secret.Encrypt(a.key, []byte(token))
	if err != nil {
		a.errorPage(w, http.StatusInternalServerError, "Internal error", "Could not store the session.")
		return
	}
	now := time.Now().Unix()
	sess := store.Session{
		IDHash:    secret.Hash([]byte(raw)),
		TenantID:  dev.TenantID,
		DeviceID:  dev.ID,
		Token:     enc,
		CreatedAt: now,
		LastSeen:  now,
		ExpiresAt: now + int64(a.cfg.SessionTTL.Seconds()),
		UA:        truncate(r.UserAgent(), 200),
	}
	if err := a.store.CreateSession(sess); err != nil {
		a.errorPage(w, http.StatusInternalServerError, "Internal error", "Could not create a session.")
		return
	}
	a.setCookie(w, raw)
	_ = a.store.TouchDevice(dev.ID)
	http.Redirect(w, r, "/", http.StatusFound)
}

func (a *App) pairRedeem(ctx context.Context, conn *tunnel.Conn, code string) (string, error) {
	var status int
	var buf bytes.Buffer
	err := conn.Do(ctx,
		tunnel.Frame{
			T:       "req",
			Method:  http.MethodGet,
			Path:    "/auth/connect/" + url.PathEscape(code),
			Headers: [][2]string{{"accept", "application/json"}},
		},
		func(f tunnel.Frame) error {
			status = f.Status
			return nil
		},
		func(b []byte) error {
			if buf.Len()+len(b) > 1<<20 {
				return errors.New("pair response too large")
			}
			_, _ = buf.Write(b)
			return nil
		})
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("pair redeem: status %d", status)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		return "", err
	}
	if out.Token == "" {
		return "", errors.New("pair redeem: empty token")
	}
	return out.Token, nil
}

func (a *App) mintRaw(ctx context.Context, deviceID string) (string, int, error) {
	conn := a.hub.Get(deviceID)
	if conn == nil {
		return "", 0, errOffline
	}
	f, err := conn.Mint(ctx)
	if err != nil {
		return "", 0, err
	}
	if f.Code == "" {
		return "", 0, errors.New("empty pair code")
	}
	exp := f.ExpiresIn
	if exp <= 0 || exp > 300 {
		exp = 300
	}
	return f.Code, exp, nil
}

func (a *App) mintPair(ctx context.Context, sess *store.Session) (string, int64, error) {
	code, exp, err := a.mintRaw(ctx, sess.DeviceID)
	if err != nil {
		return "", 0, err
	}
	expAt := time.Now().Unix() + int64(exp)
	if err := a.store.CreatePairCode(secret.Hash([]byte(code)), sess.DeviceID, expAt); err != nil {
		return "", 0, err
	}
	return a.cfg.PublicURL + "/_ocr/pair/" + url.PathEscape(code), expAt, nil
}

func (a *App) renew(ctx context.Context, sess *store.Session) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	code, _, err := a.mintRaw(ctx, sess.DeviceID)
	if err != nil {
		return err
	}
	conn := a.hub.Get(sess.DeviceID)
	if conn == nil {
		return errOffline
	}
	token, err := a.pairRedeem(ctx, conn, code)
	if err != nil {
		return err
	}
	enc, err := secret.Encrypt(a.key, []byte(token))
	if err != nil {
		return err
	}
	if err := a.store.UpdateSessionToken(sess.IDHash, enc); err != nil {
		return err
	}
	sess.Token = enc
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
