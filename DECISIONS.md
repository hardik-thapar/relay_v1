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

## D8 — 2026-11-12 — Parsing: outline-checked headings, parsed text committed

**Decision:** `scripts/parse.py` accepts a numbered line as a heading only if the next line looks like a title *and* the number is a plausible successor of the current section (3.2.2 → 3.2.3, 3.3 or 4). Lines repeated on over a third of pages (running headers) are dropped, as are 1–2 character figure fragments. The parsed JSON in `data/parsed/` is committed; PDFs are not (`scripts/fetch.sh` gets them at pinned versions).
**Why:** Bare numbers in these PDFs are as often page numbers or table cells as headings; the outline check rejects them without a list of special cases. Committing the parsed text pins exactly what retrieval sees, independent of the reader's `pdftotext` version (pdftotext version 26.11.0).
**Cost:** Mended end-of-line hyphens also merge genuine hyphenated words split across lines ("end-to-\nend" becomes "end-toend"). Quotes are checked against our own text, so this costs search recall, not correctness. Equations and table layout come out as flattened text.

## D9 — 2026-11-12 — Stand-in: calibrated latency, token buckets, quoting answers

**Decision:** The stand-in (`internal/standin`) resamples idle first-token times from the probe, adds 0.05 ms per prompt token and 24 ms per other request in flight, streams at 0.8 ms per output token, and refuses at RPM/TPM/RPD/TPD token buckets shaped like Groq's (including headers and 429 body). It spends 20 hidden reasoning tokens before content, so a tiny `max_tokens` gives an empty answer as on Groq. It answers by quoting the first `[chunk id] text` line of the prompt, so its answers pass verification.
**Why:** Every number traces to `results/probe/README.md`. Quoting answers keep load runs about traffic, not about redraft storms caused by a fake model writing nonsense.
**Cost / where it is least like Groq:** (1) Slowdown is a straight line fitted to two bursts; real slowness comes from other tenants' traffic and is not a function of *my* load. (2) My limits are exact; Groq let 34 of 35 burst calls through 30 RPM. (3) Failure rates are chosen, not measured: the probe saw zero failures. (4) Answer quality is meaningless, so answer grades come only from real runs. (5) Token counts are chars/5, not a tokenizer.

## D10 — 2026-11-12 — Threshold T applies to a normalised BM25 score

**Decision:** `Search` returns both the raw score and `Norm` = score / (Σ over query terms of idf·(k1+1)), the best score that query could ever get. T is compared with the top hit's `Norm`, not its raw score.
**Why:** Raw BM25 grows with the number of query terms, so one threshold cannot serve a 3-word and a 12-word question. Normalising also makes words the papers never use count against the query: "speech dijkstra" scores 0.16 against "speech model"'s 0.51 on the same chunk, though their raw scores are close (1.09 vs 1.24).
**Cost:** A long question with many filler-but-not-stopword words gets a lower `Norm` and may be refused wrongly. The rewrite step's filler removal matters more because of this.
