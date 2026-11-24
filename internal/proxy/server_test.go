package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kylebegeman/corbelo-proxy/internal/config"
)

func TestCORSPreflightAndPrivateNetworkAccess(t *testing.T) {
	srv, err := NewServer(config.Config{
		ListenAddr:     "127.0.0.1:8317",
		UpstreamURL:    "http://127.0.0.1:11434/v1",
		AllowedOrigins: []string{"https://studio.corbelo.app", "http://localhost:3000"},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Allowed origin with Chrome Private Network Access header
	req := httptest.NewRequest(http.MethodOptions, "/v1/responses", nil)
	req.Header.Set("Origin", "https://studio.corbelo.app")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "Authorization, Content-Type, X-Corbelo-Session")
	req.Header.Set("Access-Control-Request-Private-Network", "true")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 preflight, got %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://studio.corbelo.app" {
		t.Fatalf("allow origin = %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Private-Network"); got != "true" {
		t.Fatalf("allow PNA = %q, want true", got)
	}

	// Disallowed origin refused cleanly on preflight
	badReq := httptest.NewRequest(http.MethodOptions, "/v1/responses", nil)
	badReq.Header.Set("Origin", "https://evil.example.com")
	badReq.Header.Set("Access-Control-Request-Method", "POST")
	badRec := httptest.NewRecorder()
	srv.ServeHTTP(badRec, badReq)

	if badRec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for disallowed origin, got %d", badRec.Code)
	}
	if got := badRec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("unlisted origin received ACAO header: %q", got)
	}
}

func TestStreamingModelsAuthCooldownAndZeroBodyLogging(t *testing.T) {
	var upstreamHits atomic.Int32
	var receivedSession atomic.Value

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"qwen2.5-coder:14b","owned_by":"ollama"},{"id":"gpt-5.4","owned_by":"gateway"}]}`)
		case "/v1/responses":
			if s := r.Header.Get("X-Corbelo-Session"); s != "" {
				receivedSession.Store(s)
			}
			body, _ := io.ReadAll(r.Body)
			if strings.Contains(string(body), "trigger-429") {
				upstreamHits.Add(1)
				w.Header().Set("Retry-After", "45")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = io.WriteString(w, `{"error":{"message":"rate limit hit"}}`)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher, _ := w.(http.Flusher)
			_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello \"}\n\n")
			if flusher != nil {
				flusher.Flush()
			}
			_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"Corbelo\"}\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"OK\"}}]}\n\ndata: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))

	srv, err := NewServer(config.Config{
		ListenAddr:      "127.0.0.1:8317",
		UpstreamURL:     upstream.URL + "/v1",
		UpstreamAPIKey:  "upstream-secret-token-xyz",
		ClientAPIKey:    "local-client-key-987",
		AllowedOrigins:  config.DefaultAllowedOrigins,
		StaticModels:    []string{"local-default"},
		DefaultCooldown: 30 * time.Second,
		RequestTimeout:  10 * time.Second,
	}, upstream.Client(), logger)
	if err != nil {
		t.Fatal(err)
	}

	// 1. Health endpoint works without auth and leaks no keys
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "secret") || strings.Contains(rec.Body.String(), "987") {
		t.Fatalf("healthz = %d body=%s", rec.Code, rec.Body.String())
	}

	// 2. Auth enforcement
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without bearer token, got %d", rec.Code)
	}

	// 3. Model discovery merges upstream + static models
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer local-client-key-987")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("models = %d body=%s", rec.Code, rec.Body.String())
	}
	var modelsResp struct {
		Data []modelEntry `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &modelsResp); err != nil || len(modelsResp.Data) != 3 {
		t.Fatalf("unexpected models: %+v err=%v", modelsResp, err)
	}

	// 4. Streaming SSE passthrough on /v1/responses + session affinity
	secretPrompt := "SECRET_CAD_PROMPT_MUST_NOT_APPEAR_IN_LOGS"
	req = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.4","stream":true,"input":"`+secretPrompt+`"}`))
	req.Header.Set("Authorization", "Bearer local-client-key-987")
	req.Header.Set("X-Corbelo-Session", "sess-corbelo-42")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Hello ") || !strings.Contains(rec.Body.String(), "Corbelo") {
		t.Fatalf("responses stream = %d body=%s", rec.Code, rec.Body.String())
	}
	if got, _ := receivedSession.Load().(string); got != "sess-corbelo-42" {
		t.Fatalf("session affinity header not forwarded: %q", got)
	}

	// 5. Streaming SSE passthrough on /v1/chat/completions
	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"qwen2.5-coder:14b","stream":true}`))
	req.Header.Set("Authorization", "Bearer local-client-key-987")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "[DONE]") {
		t.Fatalf("chat completions stream = %d body=%s", rec.Code, rec.Body.String())
	}

	// 6. Structured 429 cooldown with zero silent fallback
	req1 := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"cool-model","input":"trigger-429"}`))
	req1.Header.Set("Authorization", "Bearer local-client-key-987")
	rec1 := httptest.NewRecorder()
	srv.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusTooManyRequests || rec1.Header().Get("Retry-After") != "45" || rec1.Header().Get("X-Corbelo-Cooldown-Until") == "" {
		t.Fatalf("first 429 = %d retry=%q until=%q", rec1.Code, rec1.Header().Get("Retry-After"), rec1.Header().Get("X-Corbelo-Cooldown-Until"))
	}

	// Immediate retry for same model is short-circuited by cooldown tracker (upstreamHits remains 1)
	req2 := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"cool-model","input":"trigger-429"}`))
	req2.Header.Set("Authorization", "Bearer local-client-key-987")
	rec2 := httptest.NewRecorder()
	srv.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusTooManyRequests || upstreamHits.Load() != 1 || !strings.Contains(rec2.Body.String(), "No silent fallback") {
		t.Fatalf("second 429 = %d hits=%d body=%s", rec2.Code, upstreamHits.Load(), rec2.Body.String())
	}

	// 7. Zero prompt/secret logging guarantee
	logs := logBuf.String()
	for _, sensitive := range []string{secretPrompt, "local-client-key-987", "upstream-secret-token-xyz", "sess-corbelo-42"} {
		if strings.Contains(logs, sensitive) {
			t.Fatalf("sensitive string %q leaked into logs:\n%s", sensitive, logs)
		}
	}
}
