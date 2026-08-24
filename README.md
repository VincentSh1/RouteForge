# RouteForge

RouteForge is an OpenAI-compatible AI inference gateway written in Go. It
supports streaming and synchronous chat completions through mock, OpenAI, and Anthropic
providers with explicit selection or deterministic fallback routing.

## API

- `GET /health`
- `POST /v1/chat/completions`
- `system`, `user`, and `assistant` message roles
- Streaming and non-streaming chat completions through provider interfaces
- OpenAI-style success and error responses

Authentication, persistence, caching, rate limiting, scoring-based routing,
and observability integrations are intentionally out of scope.

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
| `ROUTEFORGE_STREAM_IDLE_TIMEOUT` | `30s` |
| `ROUTEFORGE_PROVIDER` | `mock` |
| `OPENAI_API_KEY` | unset |
| `ANTHROPIC_API_KEY` | unset |
| `ROUTEFORGE_MODEL_GENERAL_OPENAI` | unset |
| `ROUTEFORGE_MODEL_GENERAL_ANTHROPIC` | unset |

## Example

```sh
curl http://localhost:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "mock-model",
    "messages": [{"role": "user", "content": "Hello"}]
  }'
```

Omitting `stream` is equivalent to setting it to `false`. Unknown JSON fields
are ignored for client compatibility. Mock token usage values are zero and are
not estimates.

## Streaming

Set `stream` to `true` to receive OpenAI-compatible Server-Sent Events. The
default mock provider requires no credentials:

```sh
curl -N http://localhost:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "routeforge/general",
    "messages": [{"role": "user", "content": "Hello"}],
    "stream": true
  }'
```

The output contains incremental chunks and a completion marker:

```text
data: {..."content":"Hello"...}

data: {..."content":" from"...}

data: {..."content":" RouteForge."...}

data: [DONE]
```

RouteForge may try another provider only before any stream content has been
emitted to the client. Once the first SSE chunk is emitted, provider selection
is committed for that response. A later provider failure terminates the stream
without fallback or an error event.

`ROUTEFORGE_PROVIDER_TIMEOUT` continues to bound synchronous provider calls.
Streaming calls are not limited to that total duration. Instead,
`ROUTEFORGE_STREAM_IDLE_TIMEOUT` cancels an upstream stream when no response
data arrives for the configured duration; each read of upstream stream data
resets the inactivity timer.

## Provider selection

`ROUTEFORGE_PROVIDER` accepts `mock`, `openai`, `anthropic`, or `auto`.
Selecting a real provider requires its corresponding API key. `auto` uses each
configured real provider at most once, in this fixed order:

1. OpenAI
2. Anthropic

Auto mode falls back only after rate limiting, timeout, or temporary
unavailability. It does not fall back after an invalid request, and it never
falls back to the mock provider.

## Model resolution

With an explicit provider, a model name that does not begin with `routeforge/`
is treated as provider-native and passed through unchanged. Provider-native
models are rejected in auto mode because they cannot be safely sent to a
different provider during fallback.

Phase 3A defines one logical model, `routeforge/general`. Configure its
provider-specific targets without embedding commercial model names in source:

```sh
export ROUTEFORGE_MODEL_GENERAL_OPENAI="your-openai-model"
export ROUTEFORGE_MODEL_GENERAL_ANTHROPIC="your-anthropic-model"
```

For every auto attempt, RouteForge starts with the original logical alias and
resolves it for that provider. For example, OpenAI receives the value from
`ROUTEFORGE_MODEL_GENERAL_OPENAI`; after an eligible failure, Anthropic receives
the separate Anthropic mapping. The first provider's model identifier is never
reused for the fallback request. Successful responses expose the original
logical alias to the client.

Auto mode requires a `routeforge/general` mapping for every configured
provider. Explicit provider mode can omit the mapping when it only uses native
model identifiers. The built-in mock maps `routeforge/general` to `mock-model`
for local testing.

To test OpenAI locally, use a real key only in your shell environment:

```sh
export OPENAI_API_KEY="..."
export ROUTEFORGE_PROVIDER="openai"
export ROUTEFORGE_MODEL_GENERAL_OPENAI="your-openai-model"
go run ./cmd/routeforge
```

For Anthropic:

```sh
export ANTHROPIC_API_KEY="..."
export ROUTEFORGE_PROVIDER="anthropic"
export ROUTEFORGE_MODEL_GENERAL_ANTHROPIC="your-anthropic-model"
go run ./cmd/routeforge
```

Do not commit keys or `.env` files. Automated tests use local fake upstream
servers and never make paid API calls.

RouteForge has no authentication or rate limiting. Do not expose it
to public or untrusted networks. A deployment must explicitly configure
`ROUTEFORGE_ADDR` to listen on a non-loopback interface.

## Test

```sh
go test ./...
go vet ./...
go test -race ./...
```
