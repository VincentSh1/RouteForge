# RouteForge

RouteForge is an OpenAI-compatible AI inference gateway written in Go. It
supports synchronous chat completions through mock, OpenAI, and Anthropic
providers with explicit selection or deterministic fallback routing.

## API

- `GET /health`
- `POST /v1/chat/completions`
- `system`, `user`, and `assistant` message roles
- Non-streaming chat completions through a provider interface
- OpenAI-style success and error responses

Streaming, authentication, persistence, caching, rate limiting, scoring-based
routing, and observability integrations are intentionally out of scope.

## Requirements

- Go 1.22 or newer

## Run

```sh
go run ./cmd/routeforge
```

The server listens on `127.0.0.1:8080` by default. Configuration uses
environment variables:

| Variable | Default |
| --- | --- |
| `ROUTEFORGE_ADDR` | `127.0.0.1:8080` |
| `ROUTEFORGE_READ_TIMEOUT` | `15s` |
| `ROUTEFORGE_WRITE_TIMEOUT` | `30s` |
| `ROUTEFORGE_IDLE_TIMEOUT` | `60s` |
| `ROUTEFORGE_SHUTDOWN_TIMEOUT` | `10s` |
| `ROUTEFORGE_PROVIDER_TIMEOUT` | `30s` |
| `ROUTEFORGE_PROVIDER` | `mock` |
| `OPENAI_API_KEY` | unset |
| `ANTHROPIC_API_KEY` | unset |

## Example

```sh
curl http://localhost:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "mock-model",
    "messages": [{"role": "user", "content": "Hello"}]
  }'
```

The `model` value is passed through unchanged. Omitting `stream` is equivalent
to setting it to `false`; `stream: true` returns an OpenAI-style error because
SSE is not implemented in Phase 2. Unknown JSON fields are ignored for client
compatibility. Mock token usage values are zero and are not estimates.

## Provider selection

`ROUTEFORGE_PROVIDER` accepts `mock`, `openai`, `anthropic`, or `auto`.
Selecting a real provider requires its corresponding API key. `auto` uses each
configured real provider at most once, in this fixed order:

1. OpenAI
2. Anthropic

Auto mode falls back only after rate limiting, timeout, or temporary
unavailability. It does not fall back after an invalid request, and it never
falls back to the mock provider. The model name is passed through unchanged, so
it must be valid for every provider that may receive the request; logical model
aliases are deferred to a later phase.

To test OpenAI locally, use a real key only in your shell environment:

```sh
export OPENAI_API_KEY="..."
export ROUTEFORGE_PROVIDER="openai"
go run ./cmd/routeforge
```

For Anthropic:

```sh
export ANTHROPIC_API_KEY="..."
export ROUTEFORGE_PROVIDER="anthropic"
go run ./cmd/routeforge
```

Do not commit keys or `.env` files. Automated tests use local fake upstream
servers and never make paid API calls.

RouteForge has no authentication or rate limiting in Phase 2. Do not expose it
to public or untrusted networks. A deployment must explicitly configure
`ROUTEFORGE_ADDR` to listen on a non-loopback interface.

## Test

```sh
go test ./...
go vet ./...
go test -race ./...
```
