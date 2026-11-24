# Corbelo Proxy

A standalone, zero-dependency, MIT-licensed local AI gateway designed to work seamlessly with **Corbelo Workbench** (`https://studio.corbelo.app` or self-hosted) as well as any OpenAI- or Anthropic-compatible client.

## Why Corbelo Proxy?

When you use hosted Corbelo Workbench in your browser, you may want to connect it to a local model runner (Ollama, LM Studio, llama.cpp, vLLM, or any OpenAI-compatible CLI/local proxy) running on your own machine:

- **Browser-Direct CORS + Chrome Private Network Access (PNA)**: Responds cleanly to browser preflights with an explicit origin allowlist and `Access-Control-Allow-Private-Network: true`, so `https://studio.corbelo.app` can talk directly to `http://127.0.0.1:8317` without sending prompts or local keys through Corbelo Cloud.
- **Zero-Buffering SSE Streaming**: Streams `/v1/responses`, `/v1/chat/completions`, and `/v1/messages` token-by-token.
- **Session Affinity**: Forwards `X-Corbelo-Session` for sticky routing across local multi-worker gateways.
- **Structured Cooldowns, Zero Silent Paid Fallback**: Preserves `429` / `503` + `Retry-After` + `X-Corbelo-Cooldown-Until` and refuses to silently fall back to paid API credentials.
- **Privacy First**: Binds `127.0.0.1:8317` by default and never logs prompts, tool payloads, or API keys.

## Quick Start

### Run with Go

```bash
# Point at a local Ollama instance (default: http://127.0.0.1:11434/v1)
go run ./cmd/corbelo-proxy

# Or point at LM Studio, vLLM, or any OpenAI-compatible gateway
go run ./cmd/corbelo-proxy \
  --addr 127.0.0.1:8317 \
  --upstream http://127.0.0.1:1234/v1
```

### Build a Static Binary

```bash
CGO_ENABLED=0 go build -o bin/corbelo-proxy ./cmd/corbelo-proxy
./bin/corbelo-proxy --upstream http://127.0.0.1:11434/v1
```

### Run with Docker Compose

```bash
CORBELO_PROXY_UPSTREAM_URL=http://host.docker.internal:11434/v1 docker compose up --build
```

## Connect from Corbelo Workbench

1. Open Corbelo Workbench and open the AI drawer (`Cmd/Ctrl + J`) → **Settings** → **CLI Proxy / Local**.
2. Set **Base URL** to `http://127.0.0.1:8317/v1`.
3. If you configured `CORBELO_PROXY_API_KEY`, enter that token as the optional Bearer key.
4. Click **Discover Models** — Workbench connects directly from your browser to your local `corbelo-proxy`.

## Configuration Reference

| Environment Variable | Flag | Default | Description |
| --- | --- | --- | --- |
| `CORBELO_PROXY_ADDR` | `--addr` | `127.0.0.1:8317` | Bind address (keep loopback unless you add TLS/auth) |
| `CORBELO_PROXY_UPSTREAM_URL` | `--upstream` | `http://127.0.0.1:11434/v1` | Upstream OpenAI/Anthropic-compatible base URL |
| `CORBELO_PROXY_UPSTREAM_API_KEY` | — | *(empty)* | Optional Bearer / `x-api-key` sent to upstream |
| `CORBELO_PROXY_API_KEY` | — | *(empty)* | Optional Bearer key required from incoming clients |
| `CORBELO_PROXY_ALLOWED_ORIGINS` | `--allowed-origins` | `https://corbelo.app,https://studio.corbelo.app,http://localhost:3000,http://127.0.0.1:3000` | Explicit browser CORS origin allowlist |
| `CORBELO_PROXY_MODELS` | `--models` | *(empty)* | Optional comma-separated extra model IDs merged into `/v1/models` |
| `CORBELO_PROXY_COOLDOWN_DEFAULT_SEC` | — | `30` | Fallback cooldown duration if upstream omits `Retry-After` |
| `CORBELO_PROXY_TIMEOUT_SEC` | — | `300` | Upstream HTTP request timeout |

## Endpoints

- `GET /healthz` — Readiness and active cooldown count (no secrets)
- `GET /v1/models` — Discovers models from upstream `/models` merged with configured `CORBELO_PROXY_MODELS`
- `POST /v1/responses` — OpenAI Responses API streaming & JSON passthrough
- `POST /v1/chat/completions` — OpenAI Chat Completions streaming & JSON passthrough
- `POST /v1/messages` — Anthropic Messages streaming & JSON passthrough

## License

MIT — see [LICENSE](./LICENSE).
