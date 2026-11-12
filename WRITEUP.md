# The Relay — write-up

> **DRAFT.** **[YOU: …]** marks your own words. Graded rows use 20 **placeholder** questions; replace and re-run `eval` first. Numbers: `python3 scripts/tables.py results/results.csv`; reasons and costs: `DECISIONS.md`.

## 1. What I built and why

Go pipeline: **rewrite** (rules) → **retrieve** (own BM25) → **decide** (refuse with no model call if top normalised score < T) → **draft** (one Groq call) → **verify** (string checks, ≤1 redraft), behind a token-bucket limiter, a retry policy, one FIFO queue and a ladder: L0 full, L1 fewer chunks, L2 "busy, try in N s", L3 cited passages, no model call. **Deciding choice: under pressure give less, never something wrong.** Replies are held until every sentence cites a retrieved chunk and every quote is in its chunk word for word; uncited sentences are deleted. Cost: the first token *is* the whole answer.

## 2. The real service and my stand-in

| Limit | Value found | How found | Confidence |
|---|---|---|---|
| Requests per minute | 30 | 429 body; yet 34 of a 35-call burst passed | Value high; enforcement low |
| Tokens per minute | 8,000 | header; 8 concurrent 1.28K-token calls: 6 ok, 2 refused | High |
| Requests per day | 1,000 | header; reset grows 86.4 s per call (refill, not midnight) | High |
| Tokens per day | 200,000 | docs only | Low: not measured |
| Requests in flight | no cap up to 35 | 35 concurrent calls | Low |
| When a limit is hit | 429 in 62–237 ms; `retry-after`; body names limit and wait | captured | High |
| Time to recover | RPM 2 s; TPM shortfall ÷ 133 tokens/s | poll; arithmetic on 429 | Medium |

From 61 planned calls (`results/probe/`). The **stand-in** speaks Groq's wire format, resamples first-token delay from the probe, adds 24 ms per other request in flight, and refuses with Groq-shaped buckets, headers and 429s. **Least like Groq:** real slowness comes from other tenants, not my load; its limits are exact where Groq leaked; its failures are chosen, while real steady saw 3 in 16.

## 3. Results

| Scenario | Real or stand-in | Requests sent | Duration (s) | Offered rate (per min) | First token p50 / p95 / p99 (ms) | Total time p50 / p95 / p99 (ms) |
|---|---|---|---|---|---|---|
| Steady | real | 16 | 268 | 3.6 | 2171 / 12274 / 12274 | 2171 / 12274 / 12274 |
| Burst (20 at once) | stand-in | 32 | 199 | 9.7 | 15 / 7479 / 7605 | 15 / 7479 / 7605 |
| Sustained overload | stand-in | 48 | 221 | 13.0 | 523 / 15175 / 19286 | 523 / 17585 / 20001 |
| Per-minute limit halved | stand-in | 12 | 199 | 3.6 | 450 / 9530 / 9530 | 450 / 9530 / 9530 |
| Daily budget exhausted | stand-in | 7 | 148 | 2.8 | 18 / 547 / 547 | 18 / 547 / 547 |

| Scenario | Succeeded % | Rate-limited % | Timed out or given up % | Retries per request | Peak queue length | Tokens per answer | Cost per 1,000 answers (list price) |
|---|---|---|---|---|---|---|---|
| Steady (real) | 81.2 | 0.0 | 6.2 | 0.44 | 0 | 2466 | $0.218 |
| Burst | 56.2 | 43.8 | 0.0 | 0.00 | 2 | 1396 | $0.115 |
| Sustained overload | 70.8 | 27.1 | 2.1 | 0.04 | 3 | 1193 | $0.099 |
| Limit halved | 91.7 | 8.3 | 0.0 | 0.00 | 0 | 1424 | $0.117 |
| Daily budget exhausted | 28.6 | 71.4 | 0.0 | 0.00 | 0 | 1380 | $0.114 |

Cost is $0 on the free tier; shown at $0.075/$0.30 per M tokens in/out. Real steady's other 12.5% are dropped connections; rate-limited includes L2/L3. The stand-in's steady run: p50 448 ms, 100% ok. **Quality** (held-out, 10): 40% correct and cited, 40% wrong, 20% correctly refused, 2,012 tokens/answer; by hand 6 correct, 1 wrong (`results/eval_review.md`). **Unguessable:** 34 of 35 passed a 30 RPM limit; three correct answers were refused over U+2011 in "Mamba‑2".

## 4. Where the time goes

Real steady `steady-R1-071824-0005`: 12,274 ms = **11,868 waiting** in our queue for TPM + **400 working** in the model + 6 ours, no retries; all before the first token (per-row `queue_wait_ms`, `model_ms`).

## 5. When a limit is hit

**Per minute:** our limiter holds requests; admission estimates the wait for a request *plus everyone queued ahead* and gives the best rung that finishes in the person's 20 s (L0, else L1, else L2 at once). Burst: 17 L0, 1 L1, 14 L2, no timeouts, no provider 429s. Dropped: only someone whose deadline passes while queued. **Per day:** new requests get L3; running ones finish.

## Retrieval: quality versus cost

| Setting (piece size, pieces, re-rank) | Questions asked | Correct and cited % | Wrong % | Correctly refused % | Tokens per answer | First token p50 / p95 (ms) | Total time p50 / p95 (ms) |
|---|---|---|---|---|---|---|---|
| R1: 150 words, 6, no (dev) | 10 | 40.0 | 20.0 | 30.0 | 1999 | 12243 / 21768 | 12243 / 21768 |
| R2: 300 words, 4, no (dev) | 10 | 40.0 | 20.0 | 30.0 | 2438 | 16156 / 32763 | 16156 / 32763 |
| R3: 300 words, 2, no (dev) | 10 | 20.0 | 30.0 | 30.0 | 1785 | 9311 / 24583 | 9311 / 24583 |
| R1 (held-out, chosen) | 10 | 40.0 | 40.0 | 20.0 | 2012 | 9095 / 25718 | 9095 / 25718 |

**R1:** R2's quality, 18% fewer tokens; tokens per minute binds. Latency is mostly TPM queueing. Near the limit L1 uses 2 chunks; at L3 retrieval is the answer. T = 0.13 only saves tokens: the model refused every unanswerable dev question.

## How this library will mislead a naive retriever

(1) **Shared vocabulary:** "H-Net's latency in real-time spoken dialogue?" pulled Moshi chunks; the model answered "160ms" citing Moshi, and verify passed the real quote. (2) **Table-only facts:** "Parameters of the big Transformer?": 213M sits in Table 3, flattened to unlabelled numbers; the model refused. (3) **Page furniture:** Moshi's title on every page makes "What is Moshi?" match every chunk; we strip repeated lines.

## 6. Retries

Only 429, 5xx, timeouts, dropped connections; ≤2; the provider's hint plus jitter, else full-jitter backoff; never past the deadline. **Worse:** with limits silently halved, naive retries (fixed 1 s, hint ignored) hit 3 429s instead of 1 on `…-0011` and still failed, 11.8 s vs 9.5 s. Small: under plain overload both tied, as our limiter kept calls off Groq's 429s.

## 7. What the person sees

In ~20 ms, zero tokens: *"Our daily model budget is used up, so I can't write an answer. These passages from the papers best match your question:"* plus three cited excerpts: never wrong, maybe not an answer. One request returns every 86.4 s.

## 8. What I'm unsure about, what I left out, what I learned

**Least certain:** 20 s patience; at 5 s most queued answers under load are lost and the ladder must step down earlier. **Not built:** a re-ranker; a check that a cited paper is the one the question names (would have refused q19). **[YOU: one thing new to you, in your words.]**

## Your feedback on Vaani

**[YOU: what stood out, what confused you, one change.]**

## How I worked with the assistant

**[YOU: half a page; attach the real history.]**
