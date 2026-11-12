// Command relay answers one question about the papers.
//
//	go run ./cmd/relay -q "Why are dot products scaled?"            # real Groq (GROQ_API_KEY)
//	go run ./cmd/relay -base http://localhost:8090 -q "..."         # stand-in
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"relay/internal/corpus"
	"relay/internal/llm"
	"relay/internal/pipeline"
	"relay/internal/traffic"
)

const groqURL = "https://api.groq.com/openai/v1"

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "relay:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("relay", flag.ContinueOnError)
	q := fs.String("q", "", "question")
	settingName := fs.String("setting", "R1", "retrieval setting: R1, R2 or R3 (D15)")
	base := fs.String("base", groqURL, "model API base URL (Groq or the stand-in)")
	data := fs.String("data", "data/parsed", "parsed papers directory")
	threshold := fs.Float64("t", pipeline.DefaultConfig().Threshold, "refusal threshold on normalised BM25")
	patience := fs.Duration("patience", 15*time.Second, "how long the person will wait")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *q == "" {
		return fmt.Errorf("-q is required")
	}
	key := os.Getenv("GROQ_API_KEY")
	if *base == groqURL && key == "" {
		return fmt.Errorf("GROQ_API_KEY is not set")
	}

	setting, err := pipeline.SettingByName(*settingName)
	if err != nil {
		return err
	}
	docs, err := corpus.LoadDir(*data)
	if err != nil {
		return err
	}
	chunks, err := corpus.Build(docs, setting.ChunkWords, setting.Overlap)
	if err != nil {
		return err
	}

	cfg := pipeline.DefaultConfig()
	cfg.Threshold = *threshold
	lim := traffic.NewLimiter(traffic.GroqFree, nil)
	adm := traffic.NewAdmission(lim, traffic.AdmissionConfig{MaxQueue: 50, Service: 1500 * time.Millisecond}, nil)
	defer adm.Close()
	p := pipeline.New(cfg, setting, chunks, llm.NewHTTP(*base, key), lim, adm)

	start := time.Now()
	r := p.Answer(context.Background(), *q, start.Add(*patience))

	fmt.Fprintln(out, r.Text)
	fmt.Fprintf(out, "\n-- status=%s level=%s refused=%v drafts=%d retries=%d 429s=%d tokens=%d/%d top_norm=%.3f\n",
		r.Status, r.Level, r.Refused, r.Drafts, r.Retries, r.N429, r.TokensIn, r.TokensOut, r.TopNorm)
	fmt.Fprintf(out, "-- shown after %dms (queue %dms, model %dms); %s\n",
		r.Released.Sub(start).Milliseconds(), r.QueueWait.Milliseconds(), r.ModelTime.Milliseconds(), r.Detail)
	return nil
}
