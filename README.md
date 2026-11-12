# The Relay

A Go pipeline that answers questions about three arXiv papers with citations, plus a traffic controller for model rate limits and a small lab (stand-in model server, open-loop load generator, recorder) to test it under load.

Design: [DESIGN.md](DESIGN.md). Changes since: [DECISIONS.md](DECISIONS.md).

## Requirements

- Go 1.27+
- Python 3 and `pdftotext` (poppler) for parsing the papers
- A Groq API key in `GROQ_API_KEY` (only for real runs; tests never call Groq)

## Development

```bash
make hooks   # once: run `make check` before every commit
make check   # gofmt, go vet, go test, go test -race
```
