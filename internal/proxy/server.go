package proxy

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kylebegeman/corbelo-proxy/internal/config"
)

// Version is stamped at build time via -ldflags when cutting a release.
var Version = "0.1.0"

// Server implements a lightweight, open-source, localhost-first OpenAI and
// Anthropic-compatible AI proxy gateway with CORS + Private Network Access,
// session affinity, structured cooldown reporting, and zero body logging.
type Server struct {
	cfg       config.Config
	client    *http.Client
	cors      CORSConfig
	cooldowns *CooldownTracker
	log       *slog.Logger
	handler   http.Handler
}

func NewServer(cfg config.Config, client *http.Client, log *slog.Logger) (*Server, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if client == nil {
		client = &http.Client{Timeout: cfg.RequestTimeout}
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = 16 << 20
	}
	if cfg.DefaultCooldown <= 0 {
		cfg.DefaultCooldown = 30 * time.Second
	}

	s := &Server{
		cfg:       cfg,
		client:    client,
		cors:      NewCORSConfig(cfg.AllowedOrigins),
		cooldowns: NewCooldownTracker(nil),
		log:       log,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /v1/models", s.requireAuth(s.handleModels))
	mux.HandleFunc("POST /v1/responses", s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		s.handleInference(w, r, "/responses")
	}))
	mux.HandleFunc("POST /v1/chat/completions", s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		s.handleInference(w, r, "/chat/completions")
	}))
	mux.HandleFunc("POST /v1/messages", s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		s.handleInference(w, r, "/messages")
	}))

	s.handler = s.cors.Wrap(s.logMiddleware(mux))
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.ClientAPIKey == "" {
			next(w, r)
			return
		}
		provided := extractClientToken(r)
		if len(provided) == 0 || subtle.ConstantTimeCompare([]byte(provided), []byte(s.cfg.ClientAPIKey)) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]any{
				"error": map[string]any{
					"message": "Invalid or missing proxy API key.",
					"type":    "authentication_error",
				},
			})
			return
		}
		next(w, r)
	}
}

func extractClientToken(r *http.Request) string {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	if key := strings.TrimSpace(r.Header.Get("X-API-Key")); key != "" {
		return key
	}
	return ""
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}

func (sr *statusRecorder) Flush() {
	if f, ok := sr.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// logMiddleware records strictly non-sensitive metadata (method, path, status,
// duration, hashed session tag). It never reads or logs headers, API keys, or
// request/response bodies.
func (s *Server) logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		sessionTag := ""
		if raw := strings.TrimSpace(r.Header.Get("X-Corbelo-Session")); raw != "" {
			sum := sha256.Sum256([]byte(raw))
			sessionTag = hex.EncodeToString(sum[:4])
		}
		s.log.Info("proxy_request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"session", sessionTag,
		)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	parsed, _ := url.Parse(s.cfg.UpstreamURL)
	upstreamHost := ""
	if parsed != nil {
		upstreamHost = parsed.Scheme + "://" + parsed.Host
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":              true,
		"service":         "corbelo-proxy",
		"version":         Version,
		"upstreamHost":    upstreamHost,
		"authRequired":    s.cfg.ClientAPIKey != "",
		"activeCooldowns": s.cooldowns.ActiveCount(),
	})
}

type modelEntry struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	OwnedBy string `json:"owned_by,omitempty"`
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	seen := make(map[string]modelEntry)
	for _, m := range s.cfg.StaticModels {
		seen[m] = modelEntry{ID: m, Object: "model", OwnedBy: "configured"}
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, s.cfg.UpstreamURL+"/models", nil)
	if err == nil {
		s.applyUpstreamHeaders(req, r, false)
		resp, doErr := s.client.Do(req)
		if doErr == nil {
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				var parsed struct {
					Data []struct {
						ID      string `json:"id"`
						OwnedBy string `json:"owned_by"`
					} `json:"data"`
					Models []struct {
						ID   string `json:"id"`
						Name string `json:"name"`
					} `json:"models"`
				}
				if json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&parsed) == nil {
					for _, item := range parsed.Data {
						if id := strings.TrimSpace(item.ID); id != "" {
							owner := item.OwnedBy
							if owner == "" {
								owner = "upstream"
							}
							seen[id] = modelEntry{ID: id, Object: "model", OwnedBy: owner}
						}
					}
					for _, item := range parsed.Models {
						id := strings.TrimSpace(item.ID)
						if id == "" {
							id = strings.TrimSpace(item.Name)
						}
						if id != "" {
							seen[id] = modelEntry{ID: id, Object: "model", OwnedBy: "upstream"}
						}
					}
				}
			}
		}
	}

	list := make([]modelEntry, 0, len(seen))
	for _, entry := range seen {
		list = append(list, entry)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })

	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   list,
	})
}

func (s *Server) handleInference(w http.ResponseWriter, r *http.Request, upstreamSuffix string) {
	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, s.cfg.MaxBodyBytes+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]any{"message": "Could not read request body.", "type": "invalid_request_error"},
		})
		return
	}
	if int64(len(bodyBytes)) > s.cfg.MaxBodyBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{
			"error": map[string]any{"message": "Request payload exceeds proxy size limit.", "type": "invalid_request_error"},
		})
		return
	}

	var envelope struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(bodyBytes, &envelope)
	modelKey := envelope.Model

	// Enforce active cooldown immediately without hammering upstream or falling
	// back silently to another paid provider.
	if active, retrySec, until, reason := s.cooldowns.Active(modelKey); active {
		writeCooldownError(w, retrySec, until, reason)
		return
	}

	targetURL := s.cfg.UpstreamURL + upstreamSuffix
	upReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, targetURL, bytes.NewReader(bodyBytes))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": map[string]any{"message": "Could not construct upstream request.", "type": "proxy_error"},
		})
		return
	}
	s.applyUpstreamHeaders(upReq, r, true)

	resp, err := s.client.Do(upReq)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": map[string]any{
				"message": "Upstream AI server is unreachable. Verify your local model runner or proxy URL is online.",
				"type":    "upstream_unreachable",
			},
		})
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
		retrySec, until := s.cooldowns.Record(
			modelKey,
			resp.Header.Get("Retry-After"),
			s.cfg.DefaultCooldown,
			fmt.Sprintf("Upstream returned HTTP %d", resp.StatusCode),
		)
		w.Header().Set("Retry-After", strconv.Itoa(retrySec))
		w.Header().Set("X-Corbelo-Cooldown-Until", until.UTC().Format(time.RFC3339))
	}

	if session := strings.TrimSpace(r.Header.Get("X-Corbelo-Session")); session != "" {
		w.Header().Set("X-Corbelo-Session", session)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Cache-Control", "no-store")
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		w.Header().Set("X-Accel-Buffering", "no")
	}

	w.WriteHeader(resp.StatusCode)
	streamBody(w, resp.Body)
}

func (s *Server) applyUpstreamHeaders(upReq, incoming *http.Request, hasJSONBody bool) {
	if hasJSONBody {
		ct := incoming.Header.Get("Content-Type")
		if ct == "" {
			ct = "application/json"
		}
		upReq.Header.Set("Content-Type", ct)
	}
	if accept := incoming.Header.Get("Accept"); accept != "" {
		upReq.Header.Set("Accept", accept)
	}
	if session := strings.TrimSpace(incoming.Header.Get("X-Corbelo-Session")); session != "" {
		upReq.Header.Set("X-Corbelo-Session", session)
	}
	if anthropicVersion := strings.TrimSpace(incoming.Header.Get("anthropic-version")); anthropicVersion != "" {
		upReq.Header.Set("anthropic-version", anthropicVersion)
	}

	if s.cfg.UpstreamAPIKey != "" {
		upReq.Header.Set("Authorization", "Bearer "+s.cfg.UpstreamAPIKey)
		if strings.HasSuffix(upReq.URL.Path, "/messages") {
			upReq.Header.Set("x-api-key", s.cfg.UpstreamAPIKey)
		}
	} else if s.cfg.ClientAPIKey == "" {
		// Only pass client Authorization through when proxy auth is disabled.
		if auth := strings.TrimSpace(incoming.Header.Get("Authorization")); auth != "" {
			upReq.Header.Set("Authorization", auth)
		}
		if xKey := strings.TrimSpace(incoming.Header.Get("x-api-key")); xKey != "" {
			upReq.Header.Set("x-api-key", xKey)
		}
	}
}

func streamBody(w http.ResponseWriter, src io.Reader) {
	flusher, canFlush := w.(http.Flusher)
	buf := make([]byte, 8192)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, wErr := w.Write(buf[:n]); wErr != nil {
				return
			}
			if canFlush {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

func writeCooldownError(w http.ResponseWriter, retryAfterSec int, until time.Time, reason string) {
	w.Header().Set("Retry-After", strconv.Itoa(retryAfterSec))
	w.Header().Set("X-Corbelo-Cooldown-Until", until.UTC().Format(time.RFC3339))
	writeJSON(w, http.StatusTooManyRequests, map[string]any{
		"error": map[string]any{
			"message":          fmt.Sprintf("Model is cooling down (%s). Retry in %ds. No silent fallback was triggered.", reason, retryAfterSec),
			"type":             "rate_limit_cooldown",
			"retry_after_secs": retryAfterSec,
			"cooldown_until":   until.UTC().Format(time.RFC3339),
		},
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
