package proxy

import (
	"net/http"
	"strings"
)

const (
	corsAllowMethods = "GET, POST, OPTIONS"
	corsAllowHeaders = "Authorization, Content-Type, X-Corbelo-Session, X-API-Key, anthropic-version"
	corsExposeHeader = "Retry-After, X-Corbelo-Cooldown-Until, X-Corbelo-Session"
)

// CORSConfig manages explicit browser origin allowlisting and Chrome Private
// Network Access (PNA) preflight headers for loopback browser-direct access.
type CORSConfig struct {
	allowed map[string]struct{}
	allowAll bool
}

func NewCORSConfig(origins []string) CORSConfig {
	m := make(map[string]struct{}, len(origins))
	allowAll := false
	for _, raw := range origins {
		trimmed := strings.TrimRight(strings.TrimSpace(raw), "/")
		if trimmed == "*" {
			allowAll = true
			continue
		}
		if trimmed != "" {
			m[trimmed] = struct{}{}
		}
	}
	return CORSConfig{allowed: m, allowAll: allowAll}
}

func (c CORSConfig) IsAllowed(origin string) bool {
	if origin == "" {
		return true
	}
	if c.allowAll {
		return true
	}
	_, ok := c.allowed[strings.TrimRight(strings.TrimSpace(origin), "/")]
	return ok
}

// Wrap applies strict CORS and Private Network Access headers. Unlisted origins
// receive no Access-Control-Allow-Origin header and preflights fail with 403.
func (c CORSConfig) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		w.Header().Add("Vary", "Origin")
		w.Header().Add("Vary", "Access-Control-Request-Method")
		w.Header().Add("Vary", "Access-Control-Request-Headers")
		w.Header().Add("Vary", "Access-Control-Request-Private-Network")

		if origin != "" {
			if !c.IsAllowed(origin) {
				if r.Method == http.MethodOptions {
					http.Error(w, "origin not allowed", http.StatusForbidden)
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Expose-Headers", corsExposeHeader)
			if strings.EqualFold(r.Header.Get("Access-Control-Request-Private-Network"), "true") {
				w.Header().Set("Access-Control-Allow-Private-Network", "true")
			}
		}

		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", corsAllowMethods)
			w.Header().Set("Access-Control-Allow-Headers", corsAllowHeaders)
			w.Header().Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}
