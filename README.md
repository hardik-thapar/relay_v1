# The Relay

A Go pipeline that answers questions about three arXiv papers with verified citations, a traffic controller that keeps it inside Groq's free-tier limits, and a small lab to test it: a stand-in model server, an open-loop load generator, and a recorder that writes `results/results.csv`.

One rule drives every choice: **under pressure the Relay gives less, never something wrong.**

Design: [DESIGN.md](DESIGN.md). Every change since, with its cost: [DECISIONS.md](DECISIONS.md). Probe findings and Table 1: [results/probe/README.md](results/probe/README.md).

## Requirements

- Go 1.27+, Python 3, `pdftotext` (poppler)
- `GROQ_API_KEY` in the environment, only for real runs. Tests never call Groq.

## Layout

| Path | What it is |
|---|---|
| `internal/retrieve` | Tokenizer and BM25 (own implementation) |
| `internal/pipeline` | Rewrite → retrieve → decide → draft → verify; `verify.go` is the "never wrong" check |
| `internal/traffic` | Limiter (token buckets), retry policy, admission queue + degradation ladder |
| `internal/llm` | Streaming client for any OpenAI-compatible endpoint, typed errors |
| `internal/standin` | Fake provider calibrated from the probe |
| `internal/loadgen`, `cmd/loadgen` | Open-loop load generator, scenarios, graded eval |
| `internal/corpus`, `scripts/parse.py` | PDF → page/section-tagged text → chunks |
| `cmd/probe` | Planned real calls that measured the limits |
| `scripts/tables.py` | Every results table, from `results/results.csv` only |

## Reproduce

```bash
make hooks && make check                       # gofmt, vet, tests (+race), Python tests
./scripts/fetch.sh && python3 scripts/parse.py data/pdf data/parsed
go run ./cmd/relay -q "Why does the Transformer scale dot products?"     # one real question
go run ./cmd/loadgen -scenario burst                                     # stand-in load run
go run ./cmd/loadgen -scenario halved -policy naive                      # naive-retry comparison
go run ./cmd/loadgen -scenario eval -source real -split heldout          # graded real eval
python3 scripts/tables.py results/results.csv
```

Scenarios: `steady`, `burst`, `overload`, `halved` (provider limits halved without notice), `daily-exhausted`, each optionally with `-policy naive`. Only `eval` and `steady` may run against real Groq, to protect the shared daily budget.

## Status

`eval/questions.json` currently holds **placeholder** questions (`author: placeholder`); graded results from them are for building and tuning only.
