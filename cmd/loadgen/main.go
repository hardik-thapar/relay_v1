// Command loadgen runs one scenario and appends its rows to results.csv.
//
//	go run ./cmd/loadgen -scenario burst                       # stand-in (default)
//	go run ./cmd/loadgen -scenario halved -policy naive        # the naive-retry comparison
//	go run ./cmd/loadgen -scenario eval -source real -setting R1 -split dev
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"relay/internal/corpus"
	"relay/internal/eval"
	"relay/internal/llm"
	"relay/internal/loadgen"
	"relay/internal/pipeline"
	"relay/internal/results"
	"relay/internal/standin"
	"relay/internal/traffic"
)

const groqURL = "https://api.groq.com/openai/v1"

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "loadgen:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("loadgen", flag.ContinueOnError)
	name := fs.String("scenario", "", "eval, steady, burst, overload, halved, daily-exhausted, smoke")
	source := fs.String("source", "stand-in", "stand-in or real")
	settingName := fs.String("setting", "R2", "retrieval setting")
	policyName := fs.String("policy", "default", "retry policy: default or naive")
	split := fs.String("split", "all", "eval only: dev, heldout or all")
	threshold := fs.Float64("t", pipeline.DefaultConfig().Threshold, "refusal threshold on normalised BM25")
	seed := fs.Uint64("seed", 1, "arrival and question-order seed")
	outPath := fs.String("out", "results/results.csv", "results file (appended)")
	qPath := fs.String("questions", "eval/questions.json", "questions file")
	data := fs.String("data", "data/parsed", "parsed papers directory")
	if err := fs.Parse(args); err != nil {
		return err
	}

	sc, ok := scenarios[*name]
	if !ok {
		return fmt.Errorf("unknown scenario %q", *name)
	}
	if *source != "stand-in" && *source != "real" {
		return fmt.Errorf("source must be stand-in or real")
	}
	if *source == "real" && !sc.real {
		return fmt.Errorf("scenario %q would spend the shared daily budget; run it on the stand-in", *name)
	}
	policy, scenarioName := traffic.DefaultPolicy(), *name
	if *policyName == "naive" {
		policy, scenarioName = traffic.NaivePolicy(), *name+"-naive"
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
	qs, err := eval.Load(*qPath)
	if err != nil {
		return err
	}
	if err := eval.Validate(qs, docs); err != nil {
		return err
	}

	base, key := groqURL, os.Getenv("GROQ_API_KEY")
	if *source == "real" && key == "" {
		return fmt.Errorf("GROQ_API_KEY is not set")
	}
	if *source == "stand-in" {
		cfg := standin.Default()
		sc.provider(&cfg)
		cfg.Seed = *seed
		if base, err = serve(standin.New(cfg)); err != nil {
			return err
		}
		key = ""
	}

	cfg := pipeline.DefaultConfig()
	cfg.Retry, cfg.Threshold = policy, *threshold
	lim := traffic.NewLimiter(sc.limits, nil)
	adm := traffic.NewAdmission(lim, traffic.AdmissionConfig{MaxQueue: 20, Service: 1500 * time.Millisecond}, nil)
	defer adm.Close()
	p := pipeline.New(cfg, setting, chunks, llm.NewHTTP(base, key), lim, adm)

	w, err := results.Open(*outPath)
	if err != nil {
		return err
	}
	defer w.Close()

	r := loadgen.Run{Scenario: scenarioName, Source: *source, Setting: setting.Name, Patience: sc.patience}
	var rows []loadgen.Summary
	if sc.eval {
		byID := map[string]eval.Question{}
		for _, q := range qs {
			byID[q.ID] = q
		}
		r.Grade = func(id string, rep pipeline.Reply) string {
			return eval.Grade(byID[id], rep.Text, rep.Refused, rep.Cited)
		}
		// Closed loop: one question at a time, so Table 3's latencies are
		// the pipeline's, not a queue's.
		for _, q := range qs {
			if *split != "all" && q.Split != *split {
				continue
			}
			r.Jobs = []loadgen.Job{{QuestionID: q.ID, Question: q.Question}}
			s, err := r.Execute(context.Background(), p, w)
			if err != nil {
				return err
			}
			rows = append(rows, s)
		}
	} else {
		r.Jobs = jobs(sc.arrivals(*seed), qs, *seed)
		s, err := r.Execute(context.Background(), p, w)
		if err != nil {
			return err
		}
		rows = append(rows, s)
	}
	return report(out, scenarioName, rows, *outPath)
}

// jobs pairs each arrival with a question, in a seeded shuffled cycle.
func jobs(offsets []time.Duration, qs []eval.Question, seed uint64) []loadgen.Job {
	rng := rand.New(rand.NewPCG(seed, seed+1))
	order := rng.Perm(len(qs))
	out := make([]loadgen.Job, len(offsets))
	for i, off := range offsets {
		q := qs[order[i%len(qs)]]
		out[i] = loadgen.Job{Offset: off, QuestionID: q.ID, Question: q.Question}
	}
	return out
}

// serve runs the stand-in on a free local port and returns its URL.
func serve(h http.Handler) (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	go http.Serve(ln, h)
	return "http://" + ln.Addr().String(), nil
}

// report prints a one-line overview; the tables come from tables.py.
func report(out io.Writer, scenario string, sums []loadgen.Summary, path string) error {
	sent, maxLag := 0, time.Duration(0)
	for _, s := range sums {
		sent += s.Sent
		maxLag = max(maxLag, s.MaxLag)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	all, err := results.ReadAll(f)
	if err != nil {
		return err
	}
	counts := map[string]int{}
	for _, row := range all[len(all)-sent:] {
		counts[row["status"]+"/L"+row["ladder_level"]]++
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, counts[k]))
	}
	fmt.Fprintf(out, "%s: %d requests, worst start lag %dms; %s\n", scenario, sent, maxLag.Milliseconds(), strings.Join(parts, " "))
	return nil
}
