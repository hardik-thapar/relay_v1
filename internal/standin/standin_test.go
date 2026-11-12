package standin

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"relay/internal/llm"
)

// fakeClock is a settable clock shared by the server under test.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// fastConfig keeps calibrated limits but removes all delays.
func fastConfig() Config {
	c := Default()
	c.FirstTokenMS = []float64{0}
	c.InputMSPerToken, c.OutputMSPerToken, c.LoadMSPerInFlight, c.RefusalMS = 0, 0, 0, 0
	return c
}

// start runs a stand-in and returns the real llm client pointed at it:
// the same code path the pipeline uses against Groq.
func start(t *testing.T, cfg Config) (*llm.HTTPClient, *fakeClock) {
	t.Helper()
	clock := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	srv := httptest.NewServer(newWithClock(cfg, clock.now))
	t.Cleanup(srv.Close)
	return llm.NewHTTP(srv.URL, ""), clock
}

func ask(c *llm.HTTPClient, content string, maxTokens int) (*llm.Response, error) {
	return c.Complete(context.Background(), llm.Request{
		Model: "stand-in", MaxTokens: maxTokens,
		Messages: []llm.Message{{Role: "user", Content: content}},
	})
}

const prompt = "Question: what is it?\n[1706.03762v7#4] The Transformer uses multi-head attention in three different ways"

func TestAnswersByQuotingFirstChunk(t *testing.T) {
	c, _ := start(t, fastConfig())
	resp, err := ask(c, prompt, 500)
	if err != nil {
		t.Fatal(err)
	}
	want := `The papers state: "The Transformer uses multi-head attention in three different ways" [1706.03762v7#4].`
	if resp.Text != want {
		t.Errorf("text = %q", resp.Text)
	}
	if resp.TokensIn <= 72 || resp.TokensOut <= 20 {
		t.Errorf("tokens in=%d out=%d", resp.TokensIn, resp.TokensOut)
	}
	if resp.Limits.LimitTokens != 8000 || resp.Limits.LimitRequests != 1000 || resp.Limits.RemainingRequests != 999 {
		t.Errorf("limits headers: %+v", resp.Limits)
	}
}

func TestRefusesWithoutChunks(t *testing.T) {
	c, _ := start(t, fastConfig())
	resp, err := ask(c, "What is the capital of France?", 500)
	if err != nil || resp.Text != "The papers do not say." {
		t.Errorf("got %v, %v", resp, err)
	}
}

func TestSmallMaxTokensGivesEmptyAnswer(t *testing.T) {
	c, _ := start(t, fastConfig()) // ReasoningTokens = 20
	resp, err := ask(c, prompt, 16)
	if err != nil || resp.Text != "" || resp.TokensOut != 16 {
		t.Errorf("got text=%q out=%d err=%v", resp.Text, resp.TokensOut, err)
	}
}

func TestRPMLimitAndRefill(t *testing.T) {
	cfg := fastConfig()
	cfg.RPM = 2
	c, clock := start(t, cfg)
	for i := 0; i < 2; i++ {
		if _, err := ask(c, "hi", 100); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	_, err := ask(c, "hi", 100)
	var rl *llm.RateLimitError
	if !errors.As(err, &rl) || rl.RetryAfter != 30*time.Second || !strings.Contains(rl.Body, `"type":"requests"`) {
		t.Fatalf("want RPM 429 with retry-after 30s, got %T %v", err, err)
	}
	clock.add(30 * time.Second) // one request refills every 30s at 2 RPM
	if _, err := ask(c, "hi", 100); err != nil {
		t.Errorf("after refill: %v", err)
	}
}

func TestTPMLimitIsTokens(t *testing.T) {
	cfg := fastConfig()
	cfg.TPM = 200
	c, _ := start(t, cfg)
	_, err := ask(c, strings.Repeat("word ", 300), 100) // ~372 tokens > 200
	var rl *llm.RateLimitError
	if !errors.As(err, &rl) || !strings.Contains(rl.Body, `"type":"tokens"`) {
		t.Errorf("want TPM 429, got %T %v", err, err)
	}
}

func TestDailyBudgetExhausted(t *testing.T) {
	cfg := fastConfig()
	cfg.StartRPD = 0
	c, clock := start(t, cfg)
	_, err := ask(c, "hi", 100)
	var rl *llm.RateLimitError
	if !errors.As(err, &rl) || rl.RetryAfter != 87*time.Second { // 86.4s, rounded up like Groq
		t.Fatalf("want RPD 429 retry-after 87s, got %T %v", err, err)
	}
	clock.add(87 * time.Second)
	if _, err := ask(c, "hi", 100); err != nil {
		t.Errorf("one request should trickle back: %v", err)
	}
}

func TestRefusedCallConsumesNothing(t *testing.T) {
	cfg := fastConfig()
	cfg.RPM, cfg.TPM = 5, 200
	c, _ := start(t, cfg)
	ask(c, strings.Repeat("word ", 300), 100) // refused on TPM
	resp, err := ask(c, "hi", 100)
	if err != nil || resp.Limits.RemainingRequests != 999 {
		t.Errorf("refused call should not count: rem=%d err=%v", resp.Limits.RemainingRequests, err)
	}
}

func TestInjectedFailures(t *testing.T) {
	cfg := fastConfig()
	cfg.ServerErrorRate = 1
	c, _ := start(t, cfg)
	var se *llm.ServerError
	if _, err := ask(c, prompt, 500); !errors.As(err, &se) {
		t.Errorf("want ServerError, got %T %v", err, err)
	}

	cfg = fastConfig()
	cfg.DropRate = 1
	c, _ = start(t, cfg)
	var ce *llm.ConnError
	if _, err := ask(c, prompt, 500); !errors.As(err, &ce) {
		t.Errorf("want ConnError, got %T %v", err, err)
	}
}

func TestInFlightCap(t *testing.T) {
	cfg := fastConfig()
	cfg.MaxInFlight = 1
	cfg.FirstTokenMS = []float64{150} // hold the first call open
	c, _ := start(t, cfg)

	done := make(chan error)
	go func() { _, err := ask(c, "hi", 100); done <- err }()
	time.Sleep(50 * time.Millisecond)
	_, err := ask(c, "hi", 100)
	var rl *llm.RateLimitError
	if !errors.As(err, &rl) {
		t.Errorf("second concurrent call: want 429, got %T %v", err, err)
	}
	if err := <-done; err != nil {
		t.Errorf("first call: %v", err)
	}
}

func TestSlowerUnderLoad(t *testing.T) {
	s := newWithClock(Config{FirstTokenMS: []float64{100}, LoadMSPerInFlight: 10, CharsPerToken: 5}, time.Now)
	var delays []time.Duration
	for i := 0; i < 3; i++ { // three calls admitted, none released
		p, ref := s.admit(&request{})
		if ref != nil {
			t.Fatal(ref.msg)
		}
		delays = append(delays, p.firstToken)
	}
	want := []time.Duration{100 * time.Millisecond, 110 * time.Millisecond, 120 * time.Millisecond}
	for i := range want {
		if delays[i] != want[i] {
			t.Errorf("call %d first-token delay %v, want %v", i, delays[i], want[i])
		}
	}
}

func TestPiecesRejoin(t *testing.T) {
	text := "one two three four five six seven"
	if got := strings.Join(pieces(text, 3), ""); got != text {
		t.Errorf("pieces lost text: %q", got)
	}
}
