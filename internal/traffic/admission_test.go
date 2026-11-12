package traffic

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"relay/internal/llm"
)

// ---- Decide: the ladder, on a fake clock ----

func newDecider(t *testing.T, l Limits, maxQueue int) (*Admission, *Limiter, *clock) {
	t.Helper()
	c := newClock()
	lim := NewLimiter(l, c.now)
	a := NewAdmission(lim, AdmissionConfig{MaxQueue: maxQueue, Service: time.Second}, c.now)
	t.Cleanup(a.Close)
	return a, lim, c
}

func TestDecideLadder(t *testing.T) {
	const full, lite = 4000, 1500
	tests := []struct {
		name    string
		setup   func(*Limiter)
		left    time.Duration // time until the person gives up
		level   Level
		retryIn time.Duration
	}{
		{"idle: full answer", func(*Limiter) {}, 10 * time.Second, L0Full, 0},
		// 7000 of 8000 TPM used: full (4000) needs 3000 more = 22.5s; lite needs 500 = 3.75s.
		{"tight minute budget: lighter answer", useTokens(7000), 10 * time.Second, L1Lite, 0},
		{"too tight even for lite: busy", useTokens(7000), 4 * time.Second, L2Busy, 3750 * time.Millisecond},
		{"provider said wait 5s: busy with its hint", func(l *Limiter) { l.PauseFor(5 * time.Second) }, 3 * time.Second, L2Busy, 5 * time.Second},
		{"day budget gone: retrieval only", func(l *Limiter) { l.Observe(dayGone) }, 10 * time.Second, L3RetrievalOnly, 0},
		{"day gone and a longer 429 pause: still retrieval only", func(l *Limiter) { l.Observe(dayGone); l.PauseFor(87 * time.Second) }, 20 * time.Second, L3RetrievalOnly, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, lim, c := newDecider(t, GroqFree, 10)
			tt.setup(lim)
			d := a.Decide(full, lite, c.now().Add(tt.left))
			if d.Level != tt.level || d.RetryIn != tt.retryIn {
				t.Errorf("got %v retryIn=%v (%s), want %v retryIn=%v", d.Level, d.RetryIn, d.Reason, tt.level, tt.retryIn)
			}
		})
	}
}

func useTokens(n int) func(*Limiter) {
	return func(l *Limiter) {
		r, _, _ := l.TryReserve(n)
		r.Finish(n)
	}
}

// dayGone is a header saying no requests are left today.
var dayGone = llm.RateLimits{RemainingRequests: 0, RemainingTokens: -1}

func TestDecideCountsTokensQueuedAhead(t *testing.T) {
	a, lim, c := newDecider(t, GroqFree, 10)
	useTokens(6000)(lim) // 2000 TPM left; the fake clock never refills it

	// Alone, a 4000-token request needs 2000 more tokens = 15s: L0 within 20s.
	if d := a.Decide(4000, 1500, c.now().Add(20*time.Second)); d.Level != L0Full {
		t.Fatalf("alone: got %v (%s)", d.Level, d.Reason)
	}

	// Two 3000-token requests join the queue and block there.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for i := 0; i < 2; i++ {
		go a.Acquire(ctx, 3000)
	}
	for a.QueueLen() < 2 {
		time.Sleep(time.Millisecond)
	}

	// Now 6000 ahead: even lite needs 6000+1500-2000 = 5500 tokens = 41.25s.
	// Together they exceed the 8000 bucket; that is a long wait, not "never".
	d := a.Decide(4000, 1500, c.now().Add(20*time.Second))
	if d.Level != L2Busy || d.QueueLen != 2 || d.RetryIn != 41250*time.Millisecond {
		t.Errorf("with queue: got %v queue=%d retryIn=%v (%s)", d.Level, d.QueueLen, d.RetryIn, d.Reason)
	}
}

func TestDecideQueueFull(t *testing.T) {
	a, _, c := newDecider(t, GroqFree, 0) // no waiting allowed at all
	if d := a.Decide(4000, 1500, c.now().Add(time.Minute)); d.Level != L2Busy || d.Reason != "queue full" {
		t.Errorf("got %v (%s)", d.Level, d.Reason)
	}
}

func TestDecideTooLargeGoesToRetrievalOnly(t *testing.T) {
	a, _, c := newDecider(t, GroqFree, 10)
	if d := a.Decide(9000, 9000, c.now().Add(time.Minute)); d.Level != L3RetrievalOnly {
		t.Errorf("got %v (%s)", d.Level, d.Reason)
	}
}

// ---- Acquire: the queue, in real time with fast limits ----

// drained returns a limiter at 600 RPM (one request every 100 ms) whose
// minute bucket is already empty, so every Acquire has to queue.
func drained(t *testing.T, maxQueue int) *Admission {
	t.Helper()
	lim := NewLimiter(Limits{RPM: 600}, nil)
	for {
		if r, _, _ := lim.TryReserve(1); r == nil {
			break
		}
	}
	a := NewAdmission(lim, AdmissionConfig{MaxQueue: maxQueue, Service: time.Second}, nil)
	t.Cleanup(a.Close)
	return a
}

func TestAcquireIsFIFO(t *testing.T) {
	a := drained(t, 10)
	var mu sync.Mutex
	var order []int
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := a.Acquire(context.Background(), 1); err != nil {
				t.Error(err)
			}
			mu.Lock()
			order = append(order, i)
			mu.Unlock()
		}()
		time.Sleep(10 * time.Millisecond) // join the queue in order 0, 1, 2
	}
	wg.Wait()
	if order[0] != 0 || order[1] != 1 || order[2] != 2 {
		t.Errorf("served in order %v, want [0 1 2]", order)
	}
}

func TestGivingUpLeavesTheQueueAtOnce(t *testing.T) {
	a := drained(t, 10)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := a.Acquire(ctx, 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline error, got %v", err)
	}
	if waited := time.Since(start); waited > 80*time.Millisecond {
		t.Errorf("gave up after %v; should leave at its deadline", waited)
	}
	if a.QueueLen() != 0 {
		t.Error("expired request still in the queue")
	}
	// The next request is not blocked by the one that left.
	if _, err := a.Acquire(context.Background(), 1); err != nil {
		t.Error(err)
	}
}

func TestAcquireQueueFull(t *testing.T) {
	a := drained(t, 1)
	go a.Acquire(context.Background(), 1) // takes the only queue slot
	time.Sleep(10 * time.Millisecond)
	if _, err := a.Acquire(context.Background(), 1); !errors.Is(err, ErrQueueFull) {
		t.Errorf("want ErrQueueFull, got %v", err)
	}
}

func TestAcquireTooLarge(t *testing.T) {
	a := NewAdmission(NewLimiter(Limits{TPM: 100}, nil), AdmissionConfig{MaxQueue: 5}, nil)
	defer a.Close()
	if _, err := a.Acquire(context.Background(), 101); !errors.Is(err, ErrTooLarge) {
		t.Errorf("want ErrTooLarge, got %v", err)
	}
}

func TestAcquireUnderConcurrency(t *testing.T) {
	a := NewAdmission(NewLimiter(Limits{RPM: 100_000, TPM: 10_000_000}, nil), AdmissionConfig{MaxQueue: 100}, nil)
	defer a.Close()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := a.Acquire(context.Background(), 100)
			if err != nil {
				t.Error(err)
				return
			}
			r.Finish(80)
		}()
	}
	wg.Wait()
	if a.QueueLen() != 0 {
		t.Errorf("queue not empty: %d", a.QueueLen())
	}
}
