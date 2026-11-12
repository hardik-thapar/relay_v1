# Decisions

Changes to DESIGN.md after 2026-11-12 are recorded here, newest last. Each entry says what was decided, why, and what it costs.

## D1 — 2026-11-12 — Standard library only for Go

**Decision:** No third-party Go modules. HTTP, streaming (SSE line reading), CSV, JSON and tests all use the standard library.
**Why:** Every line that runs is code I can explain in the live session. BM25, the limiter and the retry policy are the point of the exercise; a library would hide them.
**Cost:** A little more code for SSE parsing and table-style tests.

## D2 — 2026-11-12 — `make check` is the commit gate

**Decision:** `make check` runs `gofmt -l`, `go vet`, `go test` and `go test -race` on every package. A versioned pre-commit hook (`.githooks/pre-commit`, enabled with `make hooks`) runs it.
**Why:** DESIGN.md §11 asks for these before every commit; a hook makes it impossible to forget.
**Cost:** `-race` on all packages instead of only limiter/queue/retry. Slower by seconds, which is fine at this size.

## D3 — 2026-11-12 — Layout

**Decision:** `cmd/` holds one binary per job (`relay`, `probe`, `standin`, `loadgen`). `internal/` holds the logic, one package per concern: `corpus`, `retrieve`, `llm`, `pipeline`, `traffic`, `results`. Python lives only in `scripts/`.
**Why:** Binaries stay thin; every package can be tested without a network.
**Cost:** None worth noting.
