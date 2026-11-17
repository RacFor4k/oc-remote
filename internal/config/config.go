package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	PublicURL       string
	Listen          string
	AdminListen     string
	DataDir         string
	SessionTTL      time.Duration
	MaxBody         int64
	InsecureCookies bool
	VAPIDSubject    string
	LogLevel        string
}

func Load() (Config, error) {
	c := Config{
		PublicURL:       env("OC_REMOTE_PUBLIC_URL", "https://rc.addies.ai"),
		Listen:          env("OC_REMOTE_LISTEN", "127.0.0.1:4097"),
		AdminListen:     env("OC_REMOTE_ADMIN_LISTEN", "127.0.0.1:4098"),
		DataDir:         env("OC_REMOTE_DATA_DIR", "/data"),
		SessionTTL:      168 * time.Hour,
		MaxBody:         25 << 20,
		InsecureCookies: envBool("OC_REMOTE_INSECURE_COOKIES"),
		VAPIDSubject:    os.Getenv("OC_REMOTE_VAPID_SUBJECT"),
		LogLevel:        env("OC_REMOTE_LOG_LEVEL", "info"),
	}
	c.PublicURL = strings.TrimRight(c.PublicURL, "/")

	ttl, err := time.ParseDuration(env("OC_REMOTE_SESSION_TTL", "168h"))
	if err != nil {
		return c, fmt.Errorf("OC_REMOTE_SESSION_TTL: %w", err)
	}
	c.SessionTTL = ttl

	if v := os.Getenv("OC_REMOTE_MAX_BODY"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			return c, fmt.Errorf("OC_REMOTE_MAX_BODY: invalid %q", v)
		}
		c.MaxBody = n
	}

	u, err := url.Parse(c.PublicURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return c, fmt.Errorf("OC_REMOTE_PUBLIC_URL: invalid %q", c.PublicURL)
	}
	if c.VAPIDSubject == "" {
		c.VAPIDSubject = "mailto:admin@" + u.Hostname()
	}
	if c.SessionTTL < time.Hour {
		return c, fmt.Errorf("OC_REMOTE_SESSION_TTL: too short")
	}
	return c, nil
}

func (c Config) CookieName() string {
	if c.InsecureCookies {
		return "ocr_session"
	}
	return "__Host-ocr_session"
}

func (c Config) SecureCookies() bool {
	return !c.InsecureCookies
}

func (c Config) Host() string {
	u, err := url.Parse(c.PublicURL)
	if err != nil {
		return "rc.addies.ai"
	}
	return u.Host
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envBool(key string) bool {
	switch strings.ToLower(os.Getenv(key)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
