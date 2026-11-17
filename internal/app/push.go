package app

import (
	"encoding/json"

	"github.com/RacFor4k/oc-remote/internal/tunnel"
	webpush "github.com/SherClockHolmes/webpush-go"
)

func (a *App) loadVAPID() error {
	priv, perr := a.store.GetMeta("vapid_private")
	pub, kerr := a.store.GetMeta("vapid_public")
	if perr == nil && kerr == nil && len(priv) > 0 && len(pub) > 0 {
		a.vapidP = string(priv)
		a.vapidK = string(pub)
		return nil
	}
	p, k, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		return err
	}
	if err := a.store.SetMeta("vapid_private", []byte(p)); err != nil {
		return err
	}
	if err := a.store.SetMeta("vapid_public", []byte(k)); err != nil {
		return err
	}
	a.vapidP = p
	a.vapidK = k
	return nil
}

func (a *App) pushPermission(tenantID string, f tunnel.Frame) {
	if a.vapidP == "" || a.vapidK == "" {
		return
	}
	subs, err := a.store.ListPushSubs(tenantID)
	if err != nil || len(subs) == 0 {
		return
	}
	body := "OpenCode needs permission"
	if f.Action != "" {
		body += ": " + truncate(f.Action, 80)
	}
	payload, err := json.Marshal(map[string]string{
		"title": "oc-remote",
		"body":  body,
		"tag":   "oc-permission",
		"url":   "/",
	})
	if err != nil {
		return
	}
	for _, s := range subs {
		sub := &webpush.Subscription{
			Endpoint: s.Endpoint,
			Keys:     webpush.Keys{P256dh: s.P256dh, Auth: s.Auth},
		}
		resp, err := webpush.SendNotification(payload, sub, &webpush.Options{
			Subscriber:      a.cfg.VAPIDSubject,
			VAPIDPublicKey:  a.vapidK,
			VAPIDPrivateKey: a.vapidP,
			TTL:             300,
		})
		if err != nil {
			a.log.Warn("push send failed", "tenant", tenantID, "error", err)
			continue
		}
		if resp == nil {
			continue
		}
		if resp.StatusCode == 404 || resp.StatusCode == 410 {
			_ = a.store.DeletePushSub(s.Endpoint)
		}
		_ = resp.Body.Close()
	}
}
