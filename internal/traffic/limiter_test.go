package traffic

import (
	"sync"
	"testing"
	"time"

	"relay/internal/bucket"
	"relay/internal/llm"
)

// clock is a settable fake clock for deterministic limiter tests.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Unix(1_800_000_000, 0)} }
func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func reserve(t *testing.T, l *Limiter, tokens int) *Reservation {
	t.Helper()
	r, wait, why := l.TryReserve(tokens)
	if r == nil {
		t.Fatalf("reserve %d: blocked %v by %s", tokens, wait, why)
	}
	return r
}

func TestRequestsPerMinute(t *testing.T) {
	c := newClock()
	l := NewLimiter(GroqFree, c.now)
	for i := 0; i < 30; i++ {
		reserve(t, l, 10)
	}
	r, wait, why := l.TryReserve(10)
	if r != nil || wait != 2*time.Second || why != ByMinute { // 30/min refills one per 2s
		t.Fatalf("31st call: r=%v wait=%v why=%s", r, wait, why)
	}
	c.add(2 * time.Second)
	reserve(t, l, 10)
}

func TestTokensPerMinute(t *testing.T) {
	c := newClock()
	l := NewLimiter(GroqFree, c.now)
	reserve(t, l, 5000)
	_, wait, why := l.TryReserve(4000) // 1000 short at 8000/60 per second
	if want := 7500 * time.Millisecond; wait != want || why != ByMinute {
		t.Errorf("wait=%v why=%s, want %v minute", wait, why, want)
	}
}

func TestFinishGivesBackUnusedTokens(t *testing.T) {
	c := newClock()
	l := NewLimiter(GroqFree, c.now)
	reserve(t, l, 5000).Finish(1200)
	if wait, _ := l.Wait(1, 6800); wait != 0 {
		t.Errorf("after settling 1200 of 5000, 6800 should fit; wait=%v", wait)
	}
	if wait, _ := l.Wait(1, 6801); wait == 0 {
		t.Error("6801 should not fit")
	}
}

func TestFinishChargesOverrun(t *testing.T) {
	c := newClock()
	l := NewLimiter(GroqFree, c.now)
	reserve(t, l, 1000).Finish(3000)
	if wait, _ := l.Wait(1, 5001); wait == 0 {
		t.Error("an overrun of 2000 should be charged")
	}
}

func TestCancelReturnsEverythingOnce(t *testing.T) {
	c := newClock()
	l := NewLimiter(Limits{RPM: 1, TPM: 100}, c.now)
	r := reserve(t, l, 100)
	r.Cancel()
	r.Cancel()         // second call must not over-refund
	r.Finish(0)        // nor may a Finish after Cancel
	reserve(t, l, 100) // the request and tokens are back
	if r2, _, _ := l.TryReserve(1); r2 != nil {
		t.Error("double refund: a second request got through a 1 RPM limit")
	}
}

func TestDailyLimitIsReportedAsDay(t *testing.T) {
	c := newClock()
	l := NewLimiter(Limits{RPM: 100, RPD: 2}, c.now)
	reserve(t, l, 1)
	reserve(t, l, 1)
	_, wait, why := l.TryReserve(1)
	if why != ByDay || wait != 12*time.Hour { // 2 per day refill one per 12h
		t.Errorf("wait=%v why=%s", wait, why)
	}
}

func TestObserveOnlyLowers(t *testing.T) {
	c := newClock()
	l := NewLimiter(GroqFree, c.now)
	l.Observe(llm.RateLimits{RemainingTokens: 500, RemainingRequests: -1})
	if wait, _ := l.Wait(1, 501); wait == 0 {
		t.Error("header said 500 tokens left; 501 must wait")
	}
	l.Observe(llm.RateLimits{RemainingTokens: 7000, RemainingRequests: -1})
	if wait, _ := l.Wait(1, 501); wait == 0 {
		t.Error("a higher header value must not raise our count")
	}
	l.Observe(llm.RateLimits{RemainingTokens: -1, RemainingRequests: 0})
	if _, why := l.Wait(1, 1); why != ByDay {
		t.Errorf("remaining-requests 0 (per day) should block by day, got %q", why)
	}
}

func TestPauseFromRetryAfter(t *testing.T) {
	c := newClock()
	l := NewLimiter(GroqFree, c.now)
	l.PauseFor(3 * time.Second)
	l.PauseFor(1 * time.Second) // must not shorten
	if _, wait, why := l.TryReserve(1); wait != 3*time.Second || why != ByPause {
		t.Errorf("wait=%v why=%s", wait, why)
	}
	c.add(3 * time.Second)
	reserve(t, l, 1)
}

func TestLargerThanBucketNeverFits(t *testing.T) {
	l := NewLimiter(GroqFree, newClock().now)
	if _, wait, _ := l.TryReserve(9000); wait != bucket.Never {
		t.Errorf("9000 tokens > 8000 TPM should never fit, wait=%v", wait)
	}
}

func TestConcurrentReserveAndFinish(t *testing.T) {
	l := NewLimiter(Limits{RPM: 1000, TPM: 1_000_000}, nil)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r, _, _ := l.TryReserve(100); r != nil {
				r.Finish(50)
			}
		}()
	}
	wg.Wait()
}
