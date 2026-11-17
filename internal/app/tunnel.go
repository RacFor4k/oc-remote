package app

import (
	"net/http"
	"time"

	"github.com/RacFor4k/oc-remote/internal/secret"
	"github.com/RacFor4k/oc-remote/internal/tunnel"
	"github.com/coder/websocket"
)

func (a *App) handleTunnel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	key := r.URL.Query().Get("key")
	if key == "" {
		writeError(w, http.StatusUnauthorized, "missing key")
		return
	}
	dev, ten, err := a.store.GetDeviceByKeyHash(secret.Hash([]byte(key)))
	if err != nil {
		writeError(w, http.StatusForbidden, "invalid key")
		return
	}
	if dev.Disabled || ten.Disabled {
		writeError(w, http.StatusForbidden, "disabled")
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	ws.SetReadLimit(64 << 20)
	c := tunnel.NewConn(ws, dev.ID, ten.ID, a.ver, a.hub)
	_ = a.store.TouchDevice(dev.ID)
	a.log.Info("tunnel online", "tenant", ten.Name, "device", dev.Name)
	c.Serve(r.Context())
	_ = a.store.TouchDevice(dev.ID)
	a.log.Info("tunnel offline", "tenant", ten.Name, "device", dev.Name)
}

func (a *App) handleRegister(deviceID, tenantID string, f tunnel.Frame) {
	if f.Code == "" {
		return
	}
	exp := f.ExpiresIn
	if exp <= 0 || exp > 300 {
		exp = 300
	}
	if err := a.store.CreatePairCode(secret.Hash([]byte(f.Code)), deviceID, time.Now().Unix()+int64(exp)); err != nil {
		a.log.Warn("pair register failed", "device", deviceID, "error", err)
	}
	_ = a.store.TouchDevice(deviceID)
}

func (a *App) handleNotify(deviceID, tenantID string, f tunnel.Frame) {
	if f.Kind != "permission" {
		return
	}
	go a.pushPermission(tenantID, f)
}
