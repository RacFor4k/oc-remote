package app

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/RacFor4k/oc-remote/internal/secret"
	"github.com/RacFor4k/oc-remote/internal/store"
)

var tenantName = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,30}[a-z0-9])?$`)

type adminDevice struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Disabled bool   `json:"disabled"`
	Online   bool   `json:"online"`
	LastSeen *int64 `json:"lastSeen,omitempty"`
}

type adminTenant struct {
	ID       string        `json:"id"`
	Name     string        `json:"name"`
	Disabled bool          `json:"disabled"`
	Sessions int           `json:"sessions"`
	Push     int           `json:"push"`
	Devices  []adminDevice `json:"devices"`
}

func (a *App) adminHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(token), []byte(a.admin)) != 1 {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		switch {
		case r.URL.Path == "/admin/list" && r.Method == http.MethodGet:
			a.adminList(w, r)
		case r.URL.Path == "/admin/tenant/add" && r.Method == http.MethodPost:
			a.adminAdd(w, r)
		case r.URL.Path == "/admin/tenant/remove" && r.Method == http.MethodPost:
			a.adminRemove(w, r)
		case r.URL.Path == "/admin/tenant/disable" && r.Method == http.MethodPost:
			a.adminSetDisabled(w, r, true)
		case r.URL.Path == "/admin/tenant/enable" && r.Method == http.MethodPost:
			a.adminSetDisabled(w, r, false)
		case r.URL.Path == "/admin/device/rotate-key" && r.Method == http.MethodPost:
			a.adminRotate(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}

func (a *App) adminAdd(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name   string `json:"name"`
		Device string `json:"device"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "bad json")
		return
	}
	name := strings.ToLower(strings.TrimSpace(in.Name))
	if !tenantName.MatchString(name) {
		writeError(w, http.StatusBadRequest, "name must match [a-z0-9-] and be 1..32 chars")
		return
	}
	device := strings.TrimSpace(in.Device)
	if device == "" {
		device = name + "-laptop"
	}
	if len(device) > 64 || strings.ContainsAny(device, "\r\n\t") {
		writeError(w, http.StatusBadRequest, "invalid device name")
		return
	}
	if _, err := a.store.GetTenantByName(name); err == nil {
		writeError(w, http.StatusConflict, "tenant exists")
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "store error")
		return
	}
	ten, err := a.store.CreateTenant(name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create tenant")
		return
	}
	key, err := secret.Token(32)
	if err != nil {
		_ = a.store.DeleteTenant(ten.ID)
		writeError(w, http.StatusInternalServerError, "could not create key")
		return
	}
	key = "ocr_" + key
	dev, err := a.store.CreateDevice(ten.ID, device, secret.Hash([]byte(key)))
	if err != nil {
		_ = a.store.DeleteTenant(ten.ID)
		writeError(w, http.StatusInternalServerError, "could not create device")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"tenant": ten.Name,
		"device": dev.Name,
		"key":    key,
		"join":   fmt.Sprintf("npx oc-remote join %s %s", a.cfg.PublicURL, key),
	})
}

func (a *App) adminRemove(w http.ResponseWriter, r *http.Request) {
	ten, ok := a.adminTenantFromBody(w, r)
	if !ok {
		return
	}
	if err := a.store.DeleteTenant(ten.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "could not delete tenant")
		return
	}
	a.hub.KickTenant(ten.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) adminSetDisabled(w http.ResponseWriter, r *http.Request, disabled bool) {
	ten, ok := a.adminTenantFromBody(w, r)
	if !ok {
		return
	}
	if err := a.store.SetTenantDisabled(ten.ID, disabled); err != nil {
		writeError(w, http.StatusInternalServerError, "could not update tenant")
		return
	}
	if disabled {
		_, _ = a.store.DeleteTenantSessions(ten.ID)
		a.hub.KickTenant(ten.ID)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) adminRotate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Tenant string `json:"tenant"`
		Device string `json:"device"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "bad json")
		return
	}
	ten, err := a.store.GetTenantByName(strings.ToLower(strings.TrimSpace(in.Tenant)))
	if err != nil {
		writeError(w, http.StatusNotFound, "tenant not found")
		return
	}
	dev, err := a.store.FindDevice(ten.ID, strings.TrimSpace(in.Device))
	if err != nil {
		writeError(w, http.StatusNotFound, "device not found")
		return
	}
	key, err := secret.Token(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create key")
		return
	}
	key = "ocr_" + key
	if err := a.store.SetDeviceKey(dev.ID, secret.Hash([]byte(key))); err != nil {
		writeError(w, http.StatusInternalServerError, "could not rotate key")
		return
	}
	a.hub.Kick(dev.ID)
	writeJSON(w, http.StatusOK, map[string]string{
		"tenant": ten.Name,
		"device": dev.Name,
		"key":    key,
		"join":   fmt.Sprintf("npx oc-remote join %s %s", a.cfg.PublicURL, key),
	})
}

func (a *App) adminList(w http.ResponseWriter, r *http.Request) {
	tens, err := a.store.ListTenants()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store error")
		return
	}
	out := make([]adminTenant, 0, len(tens))
	for _, t := range tens {
		devs, _ := a.store.ListDevices(t.ID)
		sessions, _ := a.store.CountSessions(t.ID)
		push, _ := a.store.CountPushSubs(t.ID)
		view := adminTenant{
			ID:       t.ID,
			Name:     t.Name,
			Disabled: t.Disabled,
			Sessions: sessions,
			Push:     push,
			Devices:  make([]adminDevice, 0, len(devs)),
		}
		for _, d := range devs {
			view.Devices = append(view.Devices, adminDevice{
				ID:       d.ID,
				Name:     d.Name,
				Disabled: d.Disabled,
				Online:   a.hub.Online(d.ID),
				LastSeen: d.LastSeen,
			})
		}
		out = append(out, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"tenants": out})
}

func (a *App) adminTenantFromBody(w http.ResponseWriter, r *http.Request) (*store.Tenant, bool) {
	var in struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "bad json")
		return nil, false
	}
	ten, err := a.store.GetTenantByName(strings.ToLower(strings.TrimSpace(in.Name)))
	if err != nil {
		writeError(w, http.StatusNotFound, "tenant not found")
		return nil, false
	}
	return ten, true
}
