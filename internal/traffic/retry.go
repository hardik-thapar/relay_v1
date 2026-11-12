package traffic

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"relay/internal/llm"
)

// Policy says whether, when and how often to retry a failed model call.
type Policy struct {
	MaxRetries int           // retries after the first attempt
	Base       time.Duration // first backoff step
	Cap        time.Duration // largest backoff step
	Jitter     bool          // randomise waits so clients don't retry in lockstep
	UseHint    bool          // wait as long as the provider's retry-after says
	MinAttempt time.Duration // an attempt needs at least this long to be worth starting

	// Test hooks; nil means real randomness, clock and sleep.
	Rand  func() float64
	Now   func() time.Time
	Sleep func(context.Context, time.Duration) error
}

// DefaultPolicy is the real policy (DESIGN §5). MinAttempt is about the
// fastest first token the probe saw (178-220 ms).
func DefaultPolicy() Policy {
	return Policy{MaxRetries: 2, Base: 500 * time.Millisecond, Cap: 8 * time.Second,
		Jitter: true, UseHint: true, MinAttempt: 250 * time.Millisecond}
}

// NaivePolicy is the deliberately bad policy for the comparison run:
// fixed 1 s waits, no jitter, retry hint ignored. Same retry count, so
// only the timing differs.
func NaivePolicy() Policy {
	return Policy{MaxRetries: 2, Base: time.Second, Cap: time.Second}
}

// Retryable: only failures that may go away by themselves. A 4xx other
// than 429 means our request is wrong and will fail again; a cancelled
// caller has stopped listening.
func Retryable(err error) bool {
	var (
		rl *llm.RateLimitError
		se *llm.ServerError
		te *llm.TimeoutError
		ce *llm.ConnError
	)
	return errors.As(err, &rl) || errors.As(err, &se) || errors.As(err, &te) || errors.As(err, &ce)
}

// Delay is how long to wait before retry number `retry` (1, 2, ...).
//   - Provider hint (UseHint): wait the hint, plus up to Base of jitter so
//     everyone told "2 s" does not come back in the same millisecond.
//   - Otherwise exponential backoff Base*2^(retry-1), capped; with Jitter
//     it is "full jitter": a random wait between 0 and that step.
func (p Policy) Delay(retry int, err error) time.Duration {
	var rl *llm.RateLimitError
	if p.UseHint && errors.As(err, &rl) && rl.RetryAfter > 0 {
		if p.Jitter {
			return rl.RetryAfter + time.Duration(p.rand()*float64(p.Base))
		}
		return rl.RetryAfter
	}
	step := p.Base << (retry - 1)
	if step > p.Cap || step <= 0 { // <= 0 guards shift overflow
		step = p.Cap
	}
	if p.Jitter {
		return time.Duration(p.rand() * float64(step))
	}
	return step
}

// Stats describe what retrying cost one call, for results.csv.
type Stats struct {
	Retries     int           // attempts after the first
	RateLimited int           // 429s seen
	Backoff     time.Duration // total time spent waiting between attempts
}

// Do runs call, retrying per the policy. It stops when the call succeeds,
// the error is not retryable, retries run out, or the wait plus a minimal
// attempt would overrun ctx's deadline (a late answer is a lost answer).
// It returns the last error, not the backoff's.
func Do[T any](ctx context.Context, p Policy, call func(context.Context) (T, error)) (T, Stats, error) {
	var st Stats
	for attempt := 0; ; attempt++ {
		v, err := call(ctx)
		if err == nil {
			return v, st, nil
		}
		var rl *llm.RateLimitError
		if errors.As(err, &rl) {
			st.RateLimited++
		}
		if attempt >= p.MaxRetries || !Retryable(err) || ctx.Err() != nil {
			return v, st, err
		}
		d := p.Delay(attempt+1, err)
		if dl, ok := ctx.Deadline(); ok && p.now().Add(d+p.MinAttempt).After(dl) {
			return v, st, err
		}
		if p.sleep(ctx, d) != nil {
			return v, st, err
		}
		st.Retries++
		st.Backoff += d
	}
}

func (p Policy) rand() float64 {
	if p.Rand != nil {
		return p.Rand()
	}
	return rand.Float64()
}

func (p Policy) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p Policy) sleep(ctx context.Context, d time.Duration) error {
	if p.Sleep != nil {
		return p.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
