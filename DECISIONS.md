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

## D4 — 2026-11-12 — Model: `openai/gpt-oss-20b`, low reasoning effort

**Decision:** All real calls use `openai/gpt-oss-20b` with `reasoning_effort: low`.
**Why:** Groq's rate-limit page lists the same limits for every candidate chat model (30 RPM, 1K RPD, 8K TPM, 200K TPD; to be confirmed by the probe), so limits don't separate them. The 20b is the faster of the two gpt-oss sizes, and latency is what we measure. Low effort keeps reasoning tokens, which count toward TPM, small.
**Cost:** Possibly weaker answers than 120b. The eval will show whether citation quality suffers. Reasoning tokens are hidden but still delay the first visible token.

## D5 — 2026-11-12 — A fifth error type: `ConnError`

**Decision:** Besides `RateLimitError`, `ServerError`, `ClientError` and `TimeoutError` (DESIGN §4), a dropped connection is its own `ConnError`, carrying any partial text.
**Why:** DESIGN §5 retries dropped connections, and the stand-in drops them on purpose. Folding them into `ServerError` would lose the difference between "provider said no" and "the wire broke", which the results need to tell apart.
**Cost:** One more case in the retry policy.

## D6 — 2026-11-12 — Probe findings that change the design

**Finding:** TPM refills continuously (≈133 tokens/s) and RPD refills one request per 86.4 s; neither is a fixed window. Groq let 34 of 35 simultaneous calls through a 30 RPM limit. Remaining-tokens headers disagree within a burst. Details: `results/probe/README.md`.
**Decision:** (a) The limiter models token buckets, replacing DESIGN §5's sliding windows; final shape is decided when the limiter is written. (b) Our limiter is the real guard; provider 429s are a fallback. (c) Header correction trusts the lowest remaining value seen recently, never just the latest.
**Cost:** The "daily budget exhausted" scenario is not a cliff on the real service: one request trickles back every 86.4 s. The stand-in must model that refill too, or the L3 results will look worse than reality.

## D7 — 2026-11-12 — Empty answers are failures; reasoning needs headroom

**Finding:** With `max_completion_tokens: 16`, every call returned 200 OK with no content: hidden reasoning used the whole budget.
**Decision:** The draft call gets enough `max_completion_tokens` for reasoning plus answer (set from the eval). An empty draft fails verification like any other bad draft.
**Cost:** A larger max output, which matters if TPM admission counts it (open question in the probe README).
