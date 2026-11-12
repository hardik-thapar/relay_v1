// Command probe sends a modest, planned set of real requests to learn how
// the provider behaves: latency spread, rate-limit headers, what a 429
// looks like, and how long until it recovers (DESIGN.md §4).
//
// Every call is written as one JSON line to results/probe/<run>/calls.jsonl.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"relay/internal/llm"
)

const groqURL = "https://api.groq.com/openai/v1"

// config sizes every phase. Gaps are zero in tests.
type config struct {
	Model      string
	SeqN       int           // small calls, one at a time
	SeqGap     time.Duration // pause between them, keeps us under RPM
	BigN       int           // large-prompt calls, one at a time
	BigGap     time.Duration
	BigWords   int // filler size of the large prompt
	BurstN     int // tiny calls all at once, to hit RPM
	TPMN       int // large calls all at once, to hit TPM
	CoolDown   time.Duration
	RecoverGap time.Duration
	RecoverMax int
}

func defaultConfig() config {
	return config{
		Model: "openai/gpt-oss-20b",
		SeqN:  10, SeqGap: 2500 * time.Millisecond,
		BigN: 5, BigGap: 10 * time.Second, BigWords: 1100,
		BurstN: 35, TPMN: 8, CoolDown: 65 * time.Second,
		RecoverGap: 2 * time.Second, RecoverMax: 45,
	}
}

// record is one call as written to calls.jsonl.
type record struct {
	Phase        string              `json:"phase"`
	Idx          int                 `json:"idx"`
	Sent         time.Time           `json:"sent"`
	OffsetMS     int64               `json:"offset_ms"` // since run start
	FirstTokenMS int64               `json:"first_token_ms"`
	TotalMS      int64               `json:"total_ms"`
	Status       string              `json:"status"` // ok, rate_limited, server_error, client_error, timeout, conn_error
	HTTPStatus   int                 `json:"http_status,omitempty"`
	TokensIn     int                 `json:"tokens_in"`
	TokensOut    int                 `json:"tokens_out"`
	TextChars    int                 `json:"text_chars"`
	RetryAfterMS int64               `json:"retry_after_ms,omitempty"`
	Error        string              `json:"error,omitempty"`
	Body         string              `json:"body,omitempty"` // exact error body
	Headers      map[string][]string `json:"headers,omitempty"`
}

type prober struct {
	cfg   config
	c     llm.Client
	start time.Time
	mu    sync.Mutex
	out   io.Writer
	recs  []record
}

func main() {
	cfg := defaultConfig()
	flag.StringVar(&cfg.Model, "model", cfg.Model, "model id")
	base := flag.String("base", groqURL, "API base URL")
	outDir := flag.String("out", "results/probe", "output directory")
	flag.Parse()

	key := os.Getenv("GROQ_API_KEY")
	if key == "" && *base == groqURL {
		log.Fatal("GROQ_API_KEY is not set")
	}
	dir := filepath.Join(*outDir, time.Now().UTC().Format("20060102T150405Z"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, "calls.jsonl"))
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	p := &prober{cfg: cfg, c: llm.NewHTTP(*base, key), out: f}
	p.run(context.Background())
	summarize(os.Stdout, p.recs)
	fmt.Println("wrote", f.Name())
}

// run executes the phases in order. Order matters: the bursts come last
// so the quiet phases measure an unloaded service.
func (p *prober) run(ctx context.Context) {
	p.start = time.Now()
	small := p.request("Answer in one word: what colour is a clear daytime sky?", 64)
	big := p.request(filler(p.cfg.BigWords)+"\n\nSummarise the text above in two sentences.", 300)

	for i := 0; i < p.cfg.SeqN; i++ {
		p.call(ctx, "seq", i, small)
		sleep(p.cfg.SeqGap)
	}
	for i := 0; i < p.cfg.BigN; i++ {
		p.call(ctx, "big", i, big)
		sleep(p.cfg.BigGap)
	}
	p.burst(ctx, "rpm", p.cfg.BurstN, p.request("Say ok.", 16))
	p.waitRecovery(ctx, "recover-rpm", small)

	sleep(p.cfg.CoolDown) // let the minute windows empty
	p.burst(ctx, "tpm", p.cfg.TPMN, big)
	p.waitRecovery(ctx, "recover-tpm", small)
}

func (p *prober) request(prompt string, maxTokens int) llm.Request {
	return llm.Request{
		Model:           p.cfg.Model,
		Messages:        []llm.Message{{Role: "user", Content: prompt}},
		MaxTokens:       maxTokens,
		ReasoningEffort: "low",
	}
}

// burst fires n copies of req at the same moment and waits for all.
func (p *prober) burst(ctx context.Context, phase string, n int, req llm.Request) {
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.call(ctx, phase, i, req)
		}()
	}
	wg.Wait()
}

// waitRecovery polls until a call succeeds, measuring time-to-recovery.
func (p *prober) waitRecovery(ctx context.Context, phase string, req llm.Request) {
	for i := 0; i < p.cfg.RecoverMax; i++ {
		if p.call(ctx, phase, i, req).Status == "ok" {
			return
		}
		sleep(p.cfg.RecoverGap)
	}
}

// call makes one request and records it, whatever happens.
func (p *prober) call(ctx context.Context, phase string, idx int, req llm.Request) record {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	sent := time.Now()
	resp, err := p.c.Complete(ctx, req)
	end := time.Now()

	r := record{Phase: phase, Idx: idx, Sent: sent, OffsetMS: sent.Sub(p.start).Milliseconds(), TotalMS: end.Sub(sent).Milliseconds()}
	if resp != nil {
		r.TokensIn, r.TokensOut, r.TextChars = resp.TokensIn, resp.TokensOut, len(resp.Text)
		r.Headers = keepHeaders(resp.Header)
		if !resp.FirstToken.IsZero() {
			r.FirstTokenMS = resp.FirstToken.Sub(sent).Milliseconds()
		}
	}
	r.Status, r.HTTPStatus = "ok", http.StatusOK
	if err != nil {
		classifyInto(&r, err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.recs = append(p.recs, r)
	if b, jerr := json.Marshal(r); jerr == nil {
		p.out.Write(append(b, '\n'))
	}
	return r
}

// classifyInto fills the status fields from a typed llm error.
func classifyInto(r *record, err error) {
	r.Error = err.Error()
	var (
		rl *llm.RateLimitError
		se *llm.ServerError
		ce *llm.ClientError
		te *llm.TimeoutError
	)
	switch {
	case errors.As(err, &rl):
		r.Status, r.HTTPStatus, r.Body = "rate_limited", http.StatusTooManyRequests, rl.Body
		r.RetryAfterMS = rl.RetryAfter.Milliseconds()
		r.Headers = keepHeaders(rl.Header)
	case errors.As(err, &se):
		r.Status, r.HTTPStatus, r.Body = "server_error", se.Status, se.Body
	case errors.As(err, &ce):
		r.Status, r.HTTPStatus, r.Body = "client_error", ce.Status, ce.Body
	case errors.As(err, &te):
		r.Status, r.HTTPStatus = "timeout", 0
	default:
		r.Status, r.HTTPStatus = "conn_error", 0
	}
}

// keepHeaders drops cookies; everything else is kept raw for Table 1.
func keepHeaders(h http.Header) map[string][]string {
	out := map[string][]string{}
	for k, v := range h {
		if strings.EqualFold(k, "Set-Cookie") {
			continue
		}
		out[strings.ToLower(k)] = v
	}
	return out
}

// filler builds a deterministic prompt of about n words.
func filler(n int) string {
	words := strings.Fields("The transformer replaces recurrence with attention so every position can look at every other position in one step which makes training parallel and shortens the path between distant tokens")
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString(words[i%len(words)])
		b.WriteByte(' ')
	}
	return b.String()
}

func sleep(d time.Duration) {
	if d > 0 {
		time.Sleep(d)
	}
}

// summarize prints a per-phase overview; the real tables come from Python.
func summarize(w io.Writer, recs []record) {
	byPhase := map[string][]record{}
	var order []string
	for _, r := range recs {
		if _, ok := byPhase[r.Phase]; !ok {
			order = append(order, r.Phase)
		}
		byPhase[r.Phase] = append(byPhase[r.Phase], r)
	}
	for _, ph := range order {
		rs := byPhase[ph]
		counts := map[string]int{}
		var ft, tot []int64
		for _, r := range rs {
			counts[r.Status]++
			if r.Status == "ok" {
				ft = append(ft, r.FirstTokenMS)
				tot = append(tot, r.TotalMS)
			}
		}
		fmt.Fprintf(w, "%-12s n=%-3d %v  first_token p50=%dms max=%dms  total p50=%dms max=%dms\n",
			ph, len(rs), counts, pct(ft, 50), pct(ft, 100), pct(tot, 50), pct(tot, 100))
	}
}

// pct is the nearest-rank percentile; 0 for an empty slice.
func pct(xs []int64, p int) int64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]int64(nil), xs...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	rank := (p*len(s) + 99) / 100 // ceil(p/100 * n)
	if rank < 1 {
		rank = 1
	}
	return s[rank-1]
}
