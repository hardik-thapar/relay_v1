# Probe results

Run `20261112T054119Z` (`calls.jsonl`): 61 real calls to `openai/gpt-oss-20b` on my free Groq account, `reasoning_effort: low`, from India (`x-groq-region: bom`). Plus one smoke call before it (not in the file). Docs read the same day: console.groq.com/docs/rate-limits.

## Table 1: Limits found in the real service

| Limit | Value found | How found | Confidence |
|---|---|---|---|
| Requests per minute | 30 | 429 body: "requests per minute (RPM): Limit 30, Used 30". Docs agree. **But** a burst of 35 concurrent calls got 34 through; only 1 was refused. | Value high; enforcement under concurrency low (one burst) |
| Tokens per minute | 8,000 | `x-ratelimit-limit-tokens: 8000`; 429 body "Limit 8000, Used 7659, Requested 1279". 8 concurrent ~1.28K-token calls: 6 ok, 2 refused, exactly as the arithmetic predicts. | High |
| Requests per day | 1,000 | `x-ratelimit-limit-requests: 1000`. `reset-requests` grows by 86.4 s per call (= 86400/1000): a bucket refilling one request every 86.4 s, not a midnight reset. | High (value), medium (refill model) |
| Tokens per day | 200,000 (docs) | Docs only. No header carries it and I did not hit it. | Low: not measured |
| Requests in flight at once | No cap seen up to 35 | 35 concurrent calls all answered or refused for RPM, none for concurrency. | Low: only tested to 35 |
| What it returns when a limit is hit | HTTP 429, `retry-after` in whole seconds, JSON `{"error":{"message":…,"type":"requests"\|"tokens","code":"rate_limit_exceeded"}}`; message names the limit, used, requested, and a precise wait ("7.034999999s"). Refusal is fast: 62–237 ms. | Captured bodies in `calls.jsonl` | High |
| Time until it recovers | RPM: `retry-after: 2`, next poll 2 s later succeeded. TPM: depends on request size; tokens refill continuously at 8000/60 ≈ 133/s. The 1,279-token request was told 7.03 s = (1279 − 341 free) / 133.3. | Poll after 429; arithmetic on the 429 body | RPM medium; TPM medium (my recovery poll used a small request, which fit at once, so this is derived, not observed) |

## Latency (ok calls only)

| Phase | n | First token p50 / max (ms) | Total p50 / max (ms) | Tokens in / out (median) |
|---|---|---|---|---|
| seq (small, one at a time) | 10 | 358 / 432 | 359 / 434 | 84 / 16 |
| big (~1.2K-token prompt, one at a time) | 5 | 416 / 680 | 482 / 743 | 1220 / 59 |
| tpm (big, 8 at once) | 6 | 506 / 1261 | 590 / 1285 | 1220 / 59 |
| rpm (tiny, 35 at once) | 34 | no content (see below) | 1215 / 1289 | 74 / 16 |

Fast when idle; total time roughly 3.5× when 35 arrive together. 61 calls cannot support a percentile beyond the median per phase.

## Findings that change the build

1. **TPM and RPD behave as token buckets, not fixed windows.** Header `x-ratelimit-reset-tokens` on the 429 was 57.4 s = time to refill to full at 133/s. The limiter should mirror that.
2. **The provider's RPM check leaked under a simultaneous burst** (34 of 35 passed against a limit of 30). The counters look eventually consistent. Our own limiter, not the provider, has to keep us inside the limit.
3. **A 200 OK can carry an empty answer.** All 34 `rpm` calls (`max_completion_tokens: 16`) spent every token on hidden reasoning and returned no content. The draft step must leave room for reasoning, and an empty draft must be treated as a failure.
4. **Remaining-tokens headers disagree with each other inside a burst** (values jump between 5,215 and 7,910 for calls finishing in the same millisecond). Header correction (good-to-have #1) must take the minimum seen in a short window, not the latest value.
5. **TPM binds first.** At ~1.3–2K tokens per answer, 8K TPM allows ~4–6 answers a minute, well under 30 RPM.

## Not yet clear

- Whether TPM admission counts `max_completion_tokens`. The smoke call's drop (106 = 74 prompt + 32 max) suggested yes; the 429 bodies ("Requested 1279" and "1282" for identical 1,220-token prompts with max 300) suggest Groq counts an estimate near prompt + actual, not the max. I treat it as **not counting max**, medium confidence.
