package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/RacFor4k/oc-remote/internal/secret"
	"github.com/RacFor4k/oc-remote/internal/store"
	"github.com/RacFor4k/oc-remote/internal/tunnel"
)

var (
	errOffline      = errors.New("device offline")
	errUnauthorized = errors.New("unauthorized")
	errBodyTooLarge = errors.New("request body too large")
	errHTMLTooLarge = errors.New("html response too large")
)

var skipRequestHeaders = map[string]bool{
	"host":              true,
	"cookie":            true,
	"authorization":     true,
	"connection":        true,
	"keep-alive":        true,
	"proxy-connection":  true,
	"te":                true,
	"trailer":           true,
	"transfer-encoding": true,
	"upgrade":           true,
	"accept-encoding":   true,
	"content-length":    true,
}

var skipResponseHeaders = map[string]bool{
	"set-cookie":        true,
	"content-length":    true,
	"transfer-encoding": true,
	"connection":        true,
	"keep-alive":        true,
}

func (a *App) proxy(w http.ResponseWriter, r *http.Request) {
	sess, err := a.session(r)
	if err != nil {
		a.clearCookie(w)
		a.unauthorized(w, r)
		return
	}
	if a.hub.Get(sess.DeviceID) == nil {
		a.offline(w, r)
		return
	}
	body, err := readRequestBody(w, r, a.cfg.MaxBody)
	if err != nil {
		if errors.Is(err, errBodyTooLarge) {
			a.errorPage(w, http.StatusRequestEntityTooLarge, "Request too large", "The request body exceeds the relay limit.")
			return
		}
		a.errorPage(w, http.StatusBadRequest, "Bad request", "Could not read the request body.")
		return
	}

	status, wrote, err := a.proxyOnce(r.Context(), w, r, sess, body)
	if errors.Is(err, errUnauthorized) {
		if rerr := a.renew(r.Context(), sess); rerr == nil {
			status, wrote, err = a.proxyOnce(r.Context(), w, r, sess, body)
		}
	}
	switch {
	case err == nil:
	case errors.Is(err, errUnauthorized):
		if !wrote {
			a.clearCookie(w)
			a.unauthorized(w, r)
		}
	case errors.Is(err, errOffline):
		if !wrote {
			a.offline(w, r)
		}
	default:
		a.log.Warn("proxy error", "status", status, "error", err)
		if !wrote {
			a.errorPage(w, http.StatusBadGateway, "Relay error", "The relay could not complete the request.")
		}
	}
}

func (a *App) proxyOnce(ctx context.Context, w http.ResponseWriter, r *http.Request, sess *store.Session, body []byte) (int, bool, error) {
	conn := a.hub.Get(sess.DeviceID)
	if conn == nil {
		return 0, false, errOffline
	}
	token, err := secret.Decrypt(a.key, sess.Token)
	if err != nil {
		return 0, false, err
	}

	var status int
	var respHeaders [][2]string
	var wrote bool
	var unauthorized, isHTML bool
	var buf bytes.Buffer

	err = conn.Do(ctx,
		tunnel.Frame{
			T:       "req",
			Method:  r.Method,
			Path:    r.URL.RequestURI(),
			Headers: filterRequestHeaders(r.Header),
			Auth:    basicAuth(string(token)),
			Body:    body,
		},
		func(f tunnel.Frame) error {
			status = f.Status
			if status == http.StatusUnauthorized {
				unauthorized = true
				return nil
			}
			respHeaders = f.Headers
			ct := headerValue(f.Headers, "content-type")
			isHTML = strings.HasPrefix(strings.ToLower(strings.TrimSpace(ct)), "text/html")
			if isHTML {
				return nil
			}
			writeHeaders(w, f.Headers)
			w.Header().Set("X-Accel-Buffering", "no")
			w.WriteHeader(status)
			wrote = true
			flush(w)
			return nil
		},
		func(b []byte) error {
			if unauthorized {
				return nil
			}
			if isHTML {
				if buf.Len()+len(b) > 2<<20 {
					return errHTMLTooLarge
				}
				_, _ = buf.Write(b)
				return nil
			}
			if _, err := w.Write(b); err != nil {
				return err
			}
			flush(w)
			return nil
		})
	if err != nil {
		return status, wrote, err
	}
	if unauthorized {
		return status, false, errUnauthorized
	}
	if isHTML {
		enc := headerValue(respHeaders, "content-encoding")
		out := buf.Bytes()
		if enc == "" || strings.EqualFold(enc, "identity") {
			out = injectHTML(out, a.ver)
			respHeaders = withoutHeader(respHeaders, "content-encoding")
		}
		writeHeaders(w, respHeaders)
		w.Header().Set("Content-Length", strconv.Itoa(len(out)))
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(status)
		wrote = true
		if r.Method != http.MethodHead {
			_, _ = w.Write(out)
		}
		flush(w)
	}
	return status, wrote, nil
}

func readRequestBody(w http.ResponseWriter, r *http.Request, max int64) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	lr := http.MaxBytesReader(w, r.Body, max)
	b, err := io.ReadAll(lr)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, errBodyTooLarge
		}
		return nil, err
	}
	return b, nil
}

func filterRequestHeaders(h http.Header) [][2]string {
	out := make([][2]string, 0, len(h))
	for k, vs := range h {
		lk := strings.ToLower(k)
		if skipRequestHeaders[lk] || strings.HasPrefix(lk, "x-forwarded-") {
			continue
		}
		for _, v := range vs {
			out = append(out, [2]string{k, v})
		}
	}
	return out
}

func writeHeaders(w http.ResponseWriter, headers [][2]string) {
	for _, h := range headers {
		lk := strings.ToLower(h[0])
		if skipResponseHeaders[lk] || strings.HasPrefix(lk, "proxy-") {
			continue
		}
		w.Header().Add(h[0], h[1])
	}
}

func withoutHeader(headers [][2]string, name string) [][2]string {
	out := headers[:0]
	for _, h := range headers {
		if !strings.EqualFold(h[0], name) {
			out = append(out, h)
		}
	}
	return out
}

func headerValue(headers [][2]string, name string) string {
	for _, h := range headers {
		if strings.EqualFold(h[0], name) {
			return h[1]
		}
	}
	return ""
}

func injectHTML(b []byte, version string) []byte {
	tag := []byte(`<script src="/_ocr/inject.js?v=` + version + `" defer></script>`)
	lower := bytes.ToLower(b)
	if i := bytes.Index(lower, []byte("</head>")); i >= 0 {
		out := make([]byte, 0, len(b)+len(tag))
		out = append(out, b[:i]...)
		out = append(out, tag...)
		out = append(out, b[i:]...)
		return out
	}
	return append(tag, b...)
}

func basicAuth(token string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte("opencode:"+token))
}

func flush(w http.ResponseWriter) {
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}
