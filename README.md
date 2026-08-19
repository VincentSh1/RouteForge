# RouteForge

RouteForge is an OpenAI-compatible AI inference gateway written in Go. Phase 1
provides a small synchronous API backed by a local mock provider, establishing
the boundary through which real providers and routing policies can be added
later.

## Phase 1 API

- `GET /health`
- `POST /v1/chat/completions`
- `system`, `user`, and `assistant` message roles
- Non-streaming chat completions through a provider interface
- OpenAI-style success and error responses

Streaming, authentication, real LLM providers, persistence, caching, rate
limiting, routing algorithms, and observability integrations are intentionally
out of scope.

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
SSE is not implemented in Phase 1. Unknown JSON fields are ignored for client
compatibility. Mock token usage values are zero and are not estimates.

RouteForge has no authentication or rate limiting in Phase 1. Do not expose it
to public or untrusted networks. A deployment must explicitly configure
`ROUTEFORGE_ADDR` to listen on a non-loopback interface.

## Test

```sh
go test ./...
go vet ./...
```
