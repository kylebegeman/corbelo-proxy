package config

import (
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// DefaultAllowedOrigins permits browser-direct connections from hosted Corbelo
// and Corbelo Studio origins and local development servers.
var DefaultAllowedOrigins = []string{
	"https://corbelo.app",
	"https://studio.corbelo.app",
	"http://localhost:3000",
	"http://127.0.0.1:3000",
}

// Config controls the local Corbelo Proxy HTTP gateway.
type Config struct {
	ListenAddr       string
	UpstreamURL      string
	UpstreamAPIKey   string
	ClientAPIKey     string
	AllowedOrigins   []string
	StaticModels     []string
	DefaultCooldown  time.Duration
	RequestTimeout   time.Duration
	MaxBodyBytes     int64
}

// LoadFromEnv loads configuration from CORBELO_PROXY_* environment variables
// with safe loopback-only defaults.
func LoadFromEnv() Config {
	listen := envOr("CORBELO_PROXY_ADDR", "127.0.0.1:8317")
	upstream := envOr("CORBELO_PROXY_UPSTREAM_URL", "http://127.0.0.1:11434/v1")
	origins := parseCSV(os.Getenv("CORBELO_PROXY_ALLOWED_ORIGINS"))
	if len(origins) == 0 {
		origins = append([]string(nil), DefaultAllowedOrigins...)
	}
	cooldownSec := envInt("CORBELO_PROXY_COOLDOWN_DEFAULT_SEC", 30)
	if cooldownSec < 1 {
		cooldownSec = 30
	}
	timeoutSec := envInt("CORBELO_PROXY_TIMEOUT_SEC", 300)
	if timeoutSec < 5 {
		timeoutSec = 300
	}
	return Config{
		ListenAddr:      listen,
		UpstreamURL:     strings.TrimRight(upstream, "/"),
		UpstreamAPIKey:  strings.TrimSpace(os.Getenv("CORBELO_PROXY_UPSTREAM_API_KEY")),
		ClientAPIKey:    strings.TrimSpace(os.Getenv("CORBELO_PROXY_API_KEY")),
		AllowedOrigins:  origins,
		StaticModels:    parseCSV(os.Getenv("CORBELO_PROXY_MODELS")),
		DefaultCooldown: time.Duration(cooldownSec) * time.Second,
		RequestTimeout:  time.Duration(timeoutSec) * time.Second,
		MaxBodyBytes:    16 << 20, // 16 MiB (accommodates viewport screenshots)
	}
}

// Validate checks that ListenAddr and UpstreamURL are syntactically valid.
func (c Config) Validate() error {
	if strings.TrimSpace(c.ListenAddr) == "" {
		return errors.New("listen address is required")
	}
	if strings.TrimSpace(c.UpstreamURL) == "" {
		return errors.New("upstream URL is required")
	}
	parsed, err := url.Parse(c.UpstreamURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return errors.New("upstream URL must be a valid http:// or https:// URL")
	}
	return nil
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return v
}

func parseCSV(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
