# The Relay: design (v1, simple, 6-hour build)

Written 2026-11-12. Replaces the earlier long design. Anything marked **TBD** gets a number only after I measure something. Changes after today go into DECISIONS.md as new entries.

## 1. What I'm building

A Go pipeline that answers questions about three arXiv papers (1706.03762 v7, 2410.00037 v1, 2507.07955 v2), with a traffic controller for model rate limits, and a small lab to test it: a fake model server, a load generator and a recorder that writes `results.csv`.

One rule drives every choice: **under pressure the Relay gives less, never something wrong.**

Languages: **Go** for everything that runs. **Python** only for two jobs: parsing the PDFs into text with page and heading info, and turning `results.csv` into tables.

## 2. Pipeline (5 steps, one model call)

```
question
  1. Rewrite    question -> search query         (plain rules, no model)
  2. Retrieve   query -> top-k chunks            (TOOL CALL: own BM25, no model)
  3. Decide     best score < T ? refuse : go on  (BRANCH)
  4. Draft      chunks -> cited answer           (the one model call, streamed)
  5. Verify     check citations and quotes       (LOOP: at most 1 redraft)
  -> reply
```

- **Rewrite by rules** (lowercase, drop filler words, keep technical terms). Cost I will state in the write-up: only one model call per answer, so the pipeline is less "chained" than the assistant in the brief. Gain: fewer tokens, fewer limit hits, simpler.
- **Decide:** if the best BM25 score is under threshold T, reply "the papers don't say". T is TBD, tuned on the dev half of the eval. Cost: some wrong refusals, counted as `refused_wrongly`.
- **Draft:** the model gets the chunks and must write short claims, each followed by a chunk id, with direct quotes in quotation marks.
- **Verify:** string checks, no model. Every cited id was retrieved. Every quote appears word for word (after whitespace normalisation) in the chunk it cites. At least one citation unless it is a refusal. If a check fails: redraft once naming the failure; if it fails again: reply "could not verify" (a refusal). Known limit: it can't catch a correct quote used for the wrong claim.
- **Release:** buffer the full answer, verify, then show it. So time to first token is close to total time. I accept this because it keeps the "never wrong" rule simple.

## 3. Library and retrieval

- **Python parse** (`scripts/parse.py`): `pdftotext` per paper at the pinned version, keep page breaks, detect numbered headings with a simple heuristic, write one JSON file per paper with pages and headings.
- **Go chunking:** cut pages into pieces of about N words with a small overlap, never inside a word. Each chunk has `chunk_id, paper, version, section, page_start, page_end, text`. Citation shown: `[1706.03762v7, §3.2.1, p.4]` (page only if no section found).
- **BM25** written by me, no embeddings, no vector DB. Tokenizer: lowercase, split on non-letters/digits, small stopword list.
- **Three retrieval settings** (starting guesses, may change after I see data), varying piece size and number of pieces only:

| Setting | Piece size | Pieces fetched |
|---|---|---|
| R1 | about 150 words | 6 |
| R2 | about 300 words | 4 |
| R3 | about 300 words | 2 |

No re-ranker. It is one of three allowed knobs and not worth the hours.

## 4. Model client, probe and stand-in

- **One `Client` interface**, two implementations over the same HTTP code: Groq and the stand-in (same request shape, same streaming, same rate-limit headers). The pipeline can't tell them apart. Both report: sent time, first piece time, finish time, token counts.
- **Errors are typed** (`RateLimitError{RetryAfter}`, `ServerError`, `TimeoutError`, `ClientError`) because retry depends on telling them apart.
- **Probe** (`cmd/probe`): a modest number of real calls on my own free account, some at higher concurrency. Save raw headers, timings and the exact 429 body to `results/probe/`. Read Groq's docs and write them next to what I saw. This fills Table 1 with a confidence label per row. No limit is written from memory.
- **Stand-in** (`cmd/standin`): a small HTTP server that
  - is **slow**: first-token and per-piece delays resampled from the probe data,
  - **slows under load**: extra delay grows with requests in flight (simple linear fit; marked low confidence if the probe can't support it),
  - **fails**: a set rate of 5xx and dropped connections,
  - **refuses at a limit**: per-minute requests, per-minute tokens, per-day requests, in-flight cap, with a 429 and headers shaped like the real one,
  - **streams** in pieces.

  Config records which probe run each number came from. Write-up says where it is least like the real service (my guess to check: real slowness comes from other people's traffic; mine is a formula, and real limits can change).

## 5. Traffic controller

Kept small on purpose.

- **Limiter:** sliding windows for requests per minute, tokens per minute, requests per day, plus a cap on in-flight. Before sending it reserves an estimate (input tokens + max output) and releases the difference when real usage arrives.
- **Good to have #1: header correction.** The limiter corrects its own count from the provider's rate-limit headers (names from the probe).
- **Queue and admission:** one bounded FIFO queue. On arrival, estimate the wait. If it is longer than the person's remaining patience, don't queue; take the next rung down the ladder. Expired requests are dropped from the queue immediately.
- **Deadlines** (TBD, my least certain assumption): first token and whole answer. Starting guesses to test against the probe.
- **A running request finishes** unless its deadline passes.
- **Degradation ladder** (4 levels; each gives less, none is wrong):
  - **L0** full pipeline
  - **L1** fewer chunks and no redraft (if verify fails, refuse)
  - **L2** "busy, try again in N seconds", N from the provider's retry hint, not a guess
  - **L3** daily budget gone: **no model call**. Return the top retrieved passages with citations and a plain note. Costs zero tokens and can't be wrong, since it only shows text from the papers. This is what the person sees when the budget is gone.

  Skipping verify is not a rung: it saves nothing and risks a bad answer.
- **Per-minute limit hit:** the limiter holds requests back. Who waits, who gets L1, who gets L2 depends on remaining deadline. **Per-day limit hit:** every new request goes to L3; running ones finish if they can.
- **Retries:** only 429, 5xx, timeouts, dropped connections; never other 4xx. Honour the provider's retry hint, else exponential backoff with full jitter. At most 2 retries per call. Never retry if the deadline would pass first.
- **Naive-retry run** (required evidence that retrying can hurt): same overload scenario with fixed delay, no jitter, ignoring the retry hint. Compare it to the real policy.

## 6. Load generator and results

- **Open loop.** Requests start at scheduled times whether or not earlier ones finished. Latency is measured from the **scheduled** start, so a slow system can't hide itself.
- **Good to have #2: lag check.** Log how late each start was against the schedule, to show the generator wasn't the slow part.
- **Scenarios** (rates and durations TBD after the probe): steady (real small + stand-in larger), burst, sustained overload, per-minute limit halved, daily budget exhausted, plus the naive-retry overload run. That is five scenarios plus one, more than the three the brief asks for, but Table 2a lists them.
- **Real runs** share the daily allowance with the probe and eval. Plan the budget first: probe, then 3 settings x 20 questions, then one small steady run. If I hit the daily limit by accident, keep it as evidence.
- **`results.csv`**, one file, `source` column = real or stand-in. Required columns in this order:
  `request_id, scenario, source, start_time, first_token_ms, total_ms, status, retries, tokens_in, tokens_out, retrieval_setting, answer_grade`
  Extra columns after those (I'll say so in the write-up): `question_id, queue_wait_ms, model_ms, queue_len_at_arrival, ladder_level, n_429_seen, error_detail`
  - `status` is the final status: ok, rate_limited, timeout, error.
  - `answer_grade` only for eval rows: correct, wrong, refused_correctly, refused_wrongly.
- **Python `tables.py`** reads only `results.csv` and prints every table. I recompute two numbers by hand.

## 7. Eval

20 questions I write **before** looking at any retrieval output: 14 answerable (gold key phrases, expected paper and section), 6 not answerable (some close to the topic, to test T). Dev half to tune T and settings, held-out half to report. With 20 questions the numbers are rough, and I say so. Grading: a script checks key phrases and the cited chunk; I read every row where I disagree. Tokens per answer come from the provider's usage numbers.

## 8. Good to haves (3, no more)

1. Limiter corrected from provider headers (not guessed limits).
2. Open-loop generator with scheduled-start timing and lag check.
3. Retrieval-only last rung (L3): a useful reply at zero token budget.

Stretch only if ahead: sentence-level release.

## 9. Not building

Embeddings, a model-based query rewriter, a re-ranker, a global retry budget, queue priorities, an agent framework, a dashboard, Docker, auto-tuning of limits.

## 10. Hour budget: 6.0 (targets, not measured)

| Work | Hours |
|---|---|
| Repo, `make check`, `.gitignore`, first DECISIONS entries | 0.3 |
| Groq account, docs, probe, Go streaming call, Table 1 | 0.6 |
| Python parse, Go chunking, chunk store | 0.4 |
| 20 eval questions with gold answers | 0.5 |
| BM25 + tests | 0.6 |
| Pipeline: decide, draft, verify + tests | 0.7 |
| Stand-in, calibrated | 0.5 |
| Limiter, queue/admission, retry, ladder + tests | 0.9 |
| Load generator + recorder | 0.4 |
| All runs (long ones in the background) | 0.3 |
| Tables script, write-up (2 pages), README, rehearsal | 0.8 |
| **Total** | **6.0** |

If I fall behind, cut in this order: lag check, third retrieval setting, the "busy" rung. I don't cut tests, the 20 questions or any required table.

## 11. Rules I keep

- One free Groq account, mine. No tricks around limits. Key only in `GROQ_API_KEY`, never in a file, log or commit.
- Tests never call real Groq. They use the stand-in or fixed responses.
- Every table number is recomputable from `results.csv`. Anything unmeasured says "not measured".
- Before each commit: `gofmt -l .`, `go vet ./...`, `go test ./...`, and `-race` on limiter, queue, retry.

## 12. Open items and how each gets settled

| Item | Settled by |
|---|---|
| Streaming in Go works | first probe session; else a DECISIONS entry to switch |
| Header names, real limits, retry hint | probe, Table 1 |
| Deadlines | probe plus my judgement |
| Threshold T | dev half of the eval |
| Retrieval setting | Table 3 |
| Ladder triggers | which limit runs out first |
| Backoff base and cap | probe, overload run |
| Queue max length | burst and overload runs |
