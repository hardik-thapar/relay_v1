package pipeline

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"relay/internal/corpus"
	"relay/internal/llm"
	"relay/internal/standin"
	"relay/internal/traffic"
)

var lib = []corpus.Chunk{
	{ID: "1706.03762v7#9", Paper: "1706.03762", Version: 7, Section: "3.2.1", PageStart: 4, PageEnd: 4,
		Text: "We suspect that for large values of dk the dot products grow large in magnitude pushing the softmax function into regions where it has extremely small gradients"},
	{ID: "2410.00037v1#40", Paper: "2410.00037", Version: 1, Section: "3.3.1", PageStart: 10, PageEnd: 10,
		Text: "Mimi encoder projects a 24kHz waveform to a latent representation of 12.5 frames per second"},
	{ID: "2507.07955v2#3", Paper: "2507.07955", Version: 2, Section: "2.2.1", PageStart: 6, PageEnd: 6,
		Text: "The routing module implements this intuition through cosine similarity between adjacent encoder outputs"},
}

const (
	question = "Why do the dot products get scaled before the softmax?"
	good     = `They grow large: "the dot products grow large in magnitude" [1706.03762v7#9].`
	bad      = `They shrink: "the dot products shrink" [1706.03762v7#9].`
)

// fakeClient returns scripted results in order and counts calls.
type fakeClient struct {
	mu      sync.Mutex
	script  []any // string (answer) or error
	calls   int
	lastMsg []llm.Message
}

func (f *fakeClient) Complete(_ context.Context, req llm.Request) (*llm.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastMsg = req.Messages
	step := f.script[min(f.calls, len(f.script)-1)]
	f.calls++
	now := time.Now()
	if err, ok := step.(error); ok {
		return &llm.Response{Sent: now}, err
	}
	return &llm.Response{Text: step.(string), TokensIn: 500, TokensOut: 80, Sent: now, FirstToken: now, Done: now,
		Limits: llm.RateLimits{RemainingRequests: -1, RemainingTokens: -1}}, nil
}

func newPipeline(t *testing.T, client llm.Client, l traffic.Limits) (*Pipeline, *traffic.Limiter) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Threshold = 0.1
	cfg.Retry.Sleep = func(context.Context, time.Duration) error { return nil } // no real backoff in tests
	lim := traffic.NewLimiter(l, nil)
	adm := traffic.NewAdmission(lim, traffic.AdmissionConfig{MaxQueue: 10, Service: time.Second}, nil)
	t.Cleanup(adm.Close)
	return New(cfg, Settings[1], lib, client, lim, adm), lim
}

func ask(p *Pipeline, q string) Reply {
	return p.Answer(context.Background(), q, time.Now().Add(10*time.Second))
}

func TestVerifiedAnswerShowsReadableCitation(t *testing.T) {
	c := &fakeClient{script: []any{good}}
	p, _ := newPipeline(t, c, traffic.GroqFree)
	r := ask(p, question)
	if r.Status != "ok" || r.Level != traffic.L0Full || r.Refused || r.Drafts != 1 {
		t.Fatalf("reply %+v", r)
	}
	if !strings.Contains(r.Text, "[1706.03762v7, §3.2.1, p.4]") || strings.Contains(r.Text, "#9]") {
		t.Errorf("citation not rendered: %q", r.Text)
	}
	if r.TokensIn != 500 || r.TokensOut != 80 || r.Released.IsZero() {
		t.Errorf("accounting: %+v", r)
	}
}

func TestOffTopicRefusedWithoutModelCall(t *testing.T) {
	c := &fakeClient{script: []any{good}}
	p, _ := newPipeline(t, c, traffic.GroqFree)
	r := ask(p, "What is the time complexity of Dijkstra's algorithm?")
	if !r.Refused || r.Text != RefusalText || c.calls != 0 {
		t.Errorf("reply %+v, model calls %d", r, c.calls)
	}
}

func TestRedraftOnceThenSucceed(t *testing.T) {
	c := &fakeClient{script: []any{bad, good}}
	p, _ := newPipeline(t, c, traffic.GroqFree)
	r := ask(p, question)
	if r.Status != "ok" || r.Drafts != 2 || r.Refused {
		t.Fatalf("reply %+v", r)
	}
	last := c.lastMsg[len(c.lastMsg)-1].Content
	if !strings.Contains(last, "not word for word") {
		t.Errorf("redraft prompt should name the failure: %q", last)
	}
}

func TestUncitedSentencesDroppedNotRefused(t *testing.T) {
	// The first real Groq answer had this shape: a true quote plus a
	// sentence from the model's own knowledge.
	c := &fakeClient{script: []any{`Scaling keeps training numerically stable. ` + good}}
	p, _ := newPipeline(t, c, traffic.GroqFree)
	r := ask(p, question)
	if r.Refused || r.Drafts != 1 || r.Dropped != 1 || strings.Contains(r.Text, "numerically stable") {
		t.Errorf("reply %+v", r)
	}
}

func TestTwoBadDraftsRefuse(t *testing.T) {
	c := &fakeClient{script: []any{bad, bad, good}}
	p, _ := newPipeline(t, c, traffic.GroqFree)
	r := ask(p, question)
	if r.Text != CouldNotVerify || !r.Refused || r.Drafts != 2 || c.calls != 2 {
		t.Errorf("reply %+v calls %d", r, c.calls)
	}
}

func TestModelRefusalShowsOnlyRefusalSentence(t *testing.T) {
	c := &fakeClient{script: []any{"The papers do not say. But I think it is 42."}}
	p, _ := newPipeline(t, c, traffic.GroqFree)
	if r := ask(p, question); r.Text != RefusalText || !r.Refused {
		t.Errorf("reply %+v", r)
	}
}

func TestRateLimitRetriedAndPausesLimiter(t *testing.T) {
	c := &fakeClient{script: []any{&llm.RateLimitError{RetryAfter: 50 * time.Millisecond,
		Limits: llm.RateLimits{RemainingRequests: -1, RemainingTokens: -1}}, good}}
	p, _ := newPipeline(t, c, traffic.GroqFree)
	r := ask(p, question)
	if r.Status != "ok" || r.Retries != 1 || r.N429 != 1 {
		t.Errorf("reply %+v", r)
	}
	if r.QueueWait < 40*time.Millisecond {
		t.Errorf("the retry should have waited out the provider's pause in the queue, waited %v", r.QueueWait)
	}
}

func TestClientErrorIsNotRetried(t *testing.T) {
	c := &fakeClient{script: []any{&llm.ClientError{Status: 400}, good}}
	p, _ := newPipeline(t, c, traffic.GroqFree)
	if r := ask(p, question); r.Status != "error" || c.calls != 1 || r.Text != ErrorText {
		t.Errorf("reply %+v calls %d", r, c.calls)
	}
}

func TestDailyBudgetGoneGivesPassages(t *testing.T) {
	c := &fakeClient{script: []any{good}}
	p, lim := newPipeline(t, c, traffic.GroqFree)
	lim.Observe(llm.RateLimits{RemainingRequests: 0, RemainingTokens: -1})
	r := ask(p, question)
	if r.Level != traffic.L3RetrievalOnly || r.Status != "rate_limited" || c.calls != 0 || r.TokensIn != 0 {
		t.Fatalf("reply %+v calls %d", r, c.calls)
	}
	if !strings.Contains(r.Text, "[1706.03762v7, §3.2.1, p.4]") || !strings.Contains(r.Text, "dot products grow large") {
		t.Errorf("passages: %q", r.Text)
	}
}

func TestBusyWhenMinuteBudgetTooTight(t *testing.T) {
	c := &fakeClient{script: []any{good}}
	p, lim := newPipeline(t, c, traffic.GroqFree)
	lim.PauseFor(30 * time.Second)
	r := ask(p, question)
	if r.Level != traffic.L2Busy || r.Status != "rate_limited" || !strings.Contains(r.Text, "30 seconds") || c.calls != 0 {
		t.Errorf("reply %+v", r)
	}
}

func TestTimeoutStatus(t *testing.T) {
	c := &fakeClient{script: []any{&llm.TimeoutError{Phase: "first_token"}}}
	p, lim := newPipeline(t, c, traffic.GroqFree)
	start := time.Now()
	r := ask(p, question)
	if r.Status != "timeout" || r.Text != TimeoutText || c.calls != 3 {
		t.Errorf("reply %+v calls %d", r, c.calls)
	}
	// Regression: a failed call carries no headers. Its zero-value limits
	// once emptied the daily bucket, so the retry sat in the queue until
	// the deadline instead of being tried.
	if took := time.Since(start); took > time.Second {
		t.Errorf("took %v: retries should not have waited in the queue", took)
	}
	if _, why := lim.Wait(1, 1); why == traffic.ByDay {
		t.Error("a failed call must not make the limiter think the day is over")
	}
}

// End to end through the real HTTP client and the stand-in server.
func TestAgainstStandIn(t *testing.T) {
	cfg := standin.Default()
	cfg.FirstTokenMS = []float64{5}
	cfg.OutputMSPerToken, cfg.LoadMSPerInFlight, cfg.RefusalMS = 0, 0, 0
	srv := httptest.NewServer(standin.New(cfg))
	defer srv.Close()

	p, _ := newPipeline(t, llm.NewHTTP(srv.URL, ""), traffic.GroqFree)
	r := ask(p, question)
	if r.Status != "ok" || r.Refused || len(r.Cited) != 1 || r.TokensIn == 0 || r.ModelFirstToken.IsZero() {
		t.Fatalf("reply %+v", r)
	}
	if r.Released.Before(r.ModelFirstToken) {
		t.Error("buffered release cannot come before the model's first token")
	}
}

// Regression (daily-exhausted run): a 429 said "0 requests left today",
// then refunding the refused call added one back, so the next person got
// L2 "busy" instead of L3 passages.
func TestDayGoneLearnedFrom429LeadsToPassages(t *testing.T) {
	cfg := standin.Default()
	cfg.StartRPD = 0
	cfg.FirstTokenMS, cfg.RefusalMS = []float64{1}, 0
	srv := httptest.NewServer(standin.New(cfg))
	defer srv.Close()
	p, _ := newPipeline(t, llm.NewHTTP(srv.URL, ""), traffic.GroqFree)

	first := ask(p, question)
	if first.Status != "rate_limited" || first.N429 != 1 {
		t.Fatalf("first reply %+v", first)
	}
	if second := ask(p, question); second.Level != traffic.L3RetrievalOnly {
		t.Errorf("second reply: level %v (%s), want L3 passages", second.Level, second.Detail)
	}
}
