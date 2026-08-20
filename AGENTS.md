# RouteForge Engineering Rules

## Architecture

- RouteForge is a backend and AI-infrastructure project, not a demo LLM wrapper.
- Keep it a modular monolith until demonstrated complexity requires otherwise.
- Prefer idiomatic Go and the standard library where practical.
- Do not add infrastructure or abstractions before they solve a current requirement.
- Keep HTTP concerns, gateway/application logic, and provider logic separated.

## Code quality

- Prefer simple code over clever code and deletion over new abstraction during cleanup.
- Reuse existing patterns before creating helpers. Avoid pass-through wrappers and speculative interfaces.
- Keep functions focused, make names explicit, and write comments to explain why rather than narrate code.
- No resume-driven engineering: every dependency and architectural component must solve an actual requirement.

## Security and privacy

- Never commit, print, or return secrets, credentials, tokens, API keys, authorization headers, or environment values.
- Never expose raw internal errors or stack traces to API clients.
- Never include machine-specific absolute paths or usernames in committed or public output; use repository-relative paths in reports.
- Do not log complete prompts or request bodies by default.
- Bind local servers to loopback unless another interface is explicitly configured.
- Treat provider API keys and prompt data as sensitive.

## Verification

- Run `gofmt` on changed Go code.
- Run `go vet ./...` and `go test ./...`.
- Run `go test -race ./...` when concurrency-related code changes.
- Add regression tests before or alongside behavior-sensitive fixes.
- A task is not complete when relevant verification fails.

## Scope

- Do not silently implement future phases.
- Do not introduce Redis, PostgreSQL, Docker, OpenTelemetry, Prometheus, Grafana, React, production providers, or routing unless the current task explicitly requires them.
