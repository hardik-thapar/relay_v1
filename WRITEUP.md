# The Relay — write-up

> **DRAFT.** Sections marked **[YOU: …]** need your own words. Graded results use the 20 **placeholder** questions (`author: placeholder`); replace them and re-run `eval` before submitting. Every number here is recomputable with `python3 scripts/tables.py results/results.csv`.

## 1. What I built and why

A Go pipeline over the three papers: **rewrite** (rules) → **retrieve** (own BM25, the tool call) → **decide** (refuse with no model call if the best normalised score < T) → **draft** (one Groq call, `gpt-oss-20b`) → **verify** (string checks, at most one redraft). In front of the model sits a traffic controller: token-bucket **limiter**, **retry** policy, one FIFO **admission** queue and a four-rung **ladder** (L0 full, L1 fewer chunks/no redraft, L2 "busy, try in N s", L3 cited passages with no model call).

**The decision that shaped it most: under pressure the Relay gives less, never something wrong.** Answers are buffered until verified: every sentence must carry a chunk id, cited ids must have been retrieved, every quote must appear in its chunk word for word (after typography normalisation), and at least one 3+ word quote must back the answer. Uncited sentences are deleted, not shown. I gave up time-to-first-token for this: the person's first token *is* the whole verified answer.

## 2. The real service and my stand-in

I measured limits with 61 planned calls (`cmd/probe`, raw in `results/probe/`), not from memory:

| Limit | Value found | How I found it | Confidence |
|---|---|---|---|
| Requests per minute | 30 | 429 body "RPM: Limit 30, Used 30"; docs agree. A burst of 35 got 34 through | Value high; enforcement low |
| Tokens per minute | 8,000 | `x-ratelimit-limit-tokens`; 8 concurrent 1.28K-token calls: 6 ok, 2 refused, as predicted | High |
| Requests per day | 1,000 | `x-ratelimit-limit-requests`; `reset-requests` grows 86.4 s per call: refills one per 86.4 s | High |
| Tokens per day | 200,000 | Docs only; no header | Low: not measured |
| Requests in flight at once | No cap seen up to 35 | 35 concurrent calls | Low |
| What it returns when a limit is hit | HTTP 429, `retry-after` in whole seconds, JSON naming the limit and a precise wait; in 62–237 ms | Captured bodies | High |
| Time until it recovers | RPM 2 s; TPM = shortfall ÷ 133 tokens/s (7.03 s for a 1,279-token call) | Poll after 429; 429-body arithmetic | Medium |

**Stand-in** (`internal/standin`, same wire format, so the client cannot tell): first-token delay resampled from the probe, +0.05 ms per prompt token, +24 ms per other request in flight (a line through two bursts), 0.8 ms per output token; token-bucket limits with Groq-shaped headers and 429 bodies; 20 hidden reasoning tokens, so a tiny `max_tokens` returns an empty 200 exactly as Groq did; injectable 5xx and dropped connections. **Least like the real thing:** real slowness comes from other tenants, not my load; my limits are exact where Groq leaked 34 of 35; failure rates are chosen (the probe saw none, yet the real steady run then saw 1 timeout and 2 dropped connections in 16).

## 3. Results

Real = Groq free tier; stand-in = calibrated fake. "First token" is when the verified reply is released.

**Table 2a: Latency per load scenario**

| Scenario | Real or stand-in | Requests sent | Duration (s) | Offered rate (per min) | First token p50 / p95 / p99 (ms) | Total time p50 / p95 / p99 (ms) |
|---|---|---|---|---|---|---|
| Steady | real | 16 | 268 | 3.6 | 2171 / 12274 / 12274 | 2171 / 12274 / 12274 |
| Steady | stand-in | 15 | 277 | 3.3 | 448 / 688 / 688 | 448 / 688 / 688 |
| Burst (20 at once at 1:00) | stand-in | 32 | 199 | 9.7 | 15 / 7479 / 7605 | 15 / 7479 / 7605 |
| Sustained overload (3× capacity) | stand-in | 48 | 221 | 13.0 | 523 / 15175 / 19286 | 523 / 17585 / 20001 |
| Per-minute limit halved (unannounced) | stand-in | 12 | 199 | 3.6 | 450 / 9530 / 9530 | 450 / 9530 / 9530 |
| Halved, naive retries | stand-in | 12 | 199 | 3.6 | 448 / 11799 / 11799 | 448 / 11799 / 11799 |
| Daily budget exhausted | stand-in | 7 | 148 | 2.8 | 18 / 547 / 547 | 18 / 547 / 547 |

**Table 2b: Outcomes, same rows** (cost: $0 on the free tier; at Groq's list price of $0.075/$0.30 per M tokens, reasoning billed as output)

| Scenario | Succeeded % | Rate-limited % | Timed out or given up % | Retries per request | Peak queue length | Tokens per answer | Cost per 1,000 answers |
|---|---|---|---|---|---|---|---|
| Steady (real) | 81.2 | 0.0 | 6.2 | 0.44 | 0 | 2466 | $0.218 |
| Steady (stand-in) | 100.0 | 0.0 | 0.0 | 0.00 | 0 | 1416 | $0.116 |
| Burst | 56.2 | 43.8 | 0.0 | 0.00 | 2 | 1396 | $0.115 |
| Sustained overload | 70.8 | 27.1 | 2.1 | 0.04 | 3 | 1193 | $0.099 |
| Limit halved | 91.7 | 8.3 | 0.0 | 0.00 | 0 | 1424 | $0.117 |
| Halved, naive retries | 91.7 | 8.3 | 0.0 | 0.17 | 0 | 1424 | $0.117 |
| Daily budget exhausted | 28.6 | 71.4 | 0.0 | 0.00 | 0 | 1380 | $0.114 |

Real steady's missing 12.5% are 2 dropped connections (`error`). "Rate-limited" includes L2 and L3 replies: useful, but not model answers. **Answer quality** (held-out, 10 placeholder questions, script grades): 40% correct and cited, 40% wrong, 20% correctly refused, 2,012 tokens per answer. Reading every "wrong" row by hand (`results/eval_review.md`): 6 correct, 1 partly, **1 wrong**, 2 correctly refused; two "wrongs" were grader misses. **Numbers I could not have guessed:** Groq let 34 of 35 simultaneous calls through a 30 RPM limit; the day limit refills one request every 86.4 s rather than resetting; and three correct answers were refused over one character, U+2011, which the model writes in "Mamba‑2".

## 4. Where the time goes

Real steady, request `steady-R1-071824-0005`: 12,274 ms total = **11,868 ms waiting** in our queue for TPM to refill + **400 ms working** in the model + 6 ms ours, no retries. All the delay came **before the first token**: TPM (≈4 answers a minute) was spent by the requests ahead. I know from the per-row `queue_wait_ms` and `model_ms`, which the pipeline measures around `Acquire` and `Complete`. Retrying shows up in `halved-naive-…-0011`: 9,392 waiting + 397 working + **2,010 in two backoffs**.

## 5. When a limit is hit

**Per minute:** our limiter, not Groq, holds requests back (Groq's own RPM check leaked). On arrival, admission estimates the wait for this request *plus everyone queued ahead* and picks the best rung that still finishes inside the person's 20 s patience: wait for L0; take L1 (2 chunks, no redraft) if only that fits; otherwise L2 now, with N from the limiter (which includes Groq's retry hint). In the burst: 17 got L0, 1 L1, 14 an immediate L2, nobody timed out in the queue, 0 provider 429s. Who is dropped: only someone whose deadline passes while queued, and they leave the queue at that moment. **Per day:** every new request gets L3; running ones finish. The limiter learns it from the first 429's headers.

## Retrieval: quality versus cost

| Setting (piece size, pieces fetched, re-rank) | Questions asked | Correct and cited % | Wrong % | Correctly refused % | Tokens per answer | First token p50 / p95 (ms) | Total time p50 / p95 (ms) |
|---|---|---|---|---|---|---|---|
| R1: ~150 words, 6, no (dev) | 10 | 40.0 | 20.0 | 30.0 | 1999 | 12243 / 21768 | 12243 / 21768 |
| R2: ~300 words, 4, no (dev) | 10 | 40.0 | 20.0 | 30.0 | 2438 | 16156 / 32763 | 16156 / 32763 |
| R3: ~300 words, 2, no (dev) | 10 | 20.0 | 30.0 | 30.0 | 1785 | 9311 / 24583 | 9311 / 24583 |
| R1 (held-out, chosen) | 10 | 40.0 | 40.0 | 20.0 | 2012 | 9095 / 25718 | 9095 / 25718 |

**I chose R1:** same quality as R2 on dev with 18% fewer tokens, and tokens per minute is the limit that binds, so R1 serves more people per minute. R3 is cheapest but two chunks often miss the answer. Latency here is mostly queueing for TPM even one question at a time (model time p50: R1 730 ms, R2 1,333 ms). **Near the limit**, L1 drops to 2 chunks and no redraft; at L3 retrieval *is* the answer. T = 0.13 was tuned on dev by replaying one T=0 run (`scripts/tune_t.py`): it saves tokens on clearly off-topic questions; it is not the safety net, because the model itself refused every unanswerable dev question.

## How this library will mislead a naive retriever

1. **Shared vocabulary across papers.** "What end-to-end latency does H-Net reach in real-time spoken dialogue?" — "real-time spoken dialogue" is Moshi's language, so BM25 fetched Moshi chunks and the model answered "160ms" citing Moshi. The quote was real, so verify passed it: our one genuinely wrong held-out answer.
2. **Facts that live only in tables.** "How many parameters does the big Transformer have?" — 213M appears only in Table 3, which `pdftotext` flattens into a run of numbers with no row labels; the model refused rather than guess.
3. **Page furniture.** Moshi prints its title on every page; left in, "What is Moshi?" matches every chunk equally. The parser drops lines repeated on over a third of pages.

## 6. Retries

Retry only 429, 5xx, timeouts and dropped connections, never other 4xx; at most 2; wait the provider's hint plus up to 500 ms jitter, else full-jitter exponential backoff (500 ms base, 8 s cap); never start a retry that cannot finish before the deadline. **Where retrying made it worse:** with Groq's limits silently halved, the naive policy (fixed 1 s, no jitter, hint ignored) retried into a still-empty bucket: request `…-0011` saw 3 429s instead of 1, spent 2,010 ms in backoff, and still failed, 11.8 s instead of 9.5 s. Honestly, the effect is small: under plain overload both policies gave identical results, because our limiter stopped calls from reaching Groq's 429s at all.

## 7. What the person sees

At 3 pm, daily budget gone, they get within ~20 ms, with zero tokens spent: *"Our daily model budget is used up, so I can't write an answer. These passages from the papers best match your question: 1. "…40 words…" [2410.00037v1, §3.3.1, p.10] …"* It only quotes the papers, so it cannot be wrong; it may simply not answer the question. One request trickles back every 86.4 s and is served normally.

## 8. What I'm unsure about, what I left out, what I learned

**Least certain assumption:** that people wait 20 s. If they give up at 5 s, almost every queued answer under load is lost, and the ladder must move to L1/L2 much earlier (admission takes patience as an input, so this is a number, not a redesign). **Deliberately not built:** embeddings or a re-ranker (BM25 plus verify was enough to stay correct; re-ranking would not fix q19), and a check that a cited chunk's paper matches a paper the question names (cheap, would have refused q19; found too late). **Also unsure:** whether Groq counts `max_tokens` at admission (evidence points both ways); slowdown-under-load is fitted to two points. **[YOU: one thing that was new to you, taught in your own words.]**

## Your feedback on Vaani

**[YOU: visit https://www.vaani.dev/ and write a few honest lines: what stood out, what confused you, one thing you would change.]**

## How I worked with the assistant

**[YOU: attach the real conversation history, then write this half page yourself.]** Facts you can draw on:
- *Prompts that mattered:* your design doc plus "under pressure give less, never wrong"; "run the probe" (turned assumptions into Table 1); "fix and rerun" after the first Phase 9 runs.
- *Things it got wrong that were caught:* verify let an uncited sentence through (caught on the first real Groq answer); a failed call's zero-value headers emptied the day budget (a test took 10 s instead of ms); its first fix for the L2-instead-of-L3 bug was not the real cause (a reproducing test found a refund undoing Groq's headers); it misread its own smoke-call data about `max_tokens` and corrected itself.
- *Where you overruled or directed:* you switched from writing the core files yourself to having them written and explained; you chose to run with placeholder questions.
- **[YOU: what in this submission is distinctly yours.]**
