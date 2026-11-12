package traffic

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"relay/internal/llm"
)

var (
	errRL       = &llm.RateLimitError{RetryAfter: 2 * time.Second}
	errRLNoHint = &llm.RateLimitError{}
	err5xx      = &llm.ServerError{Status: 503}
	err4xx      = &llm.ClientError{Status: 400}
	errTO       = &llm.TimeoutError{Phase: "first_token"}
	errConn     = &llm.ConnError{Err: errors.New("reset")}
)

func TestRetryable(t *testing.T) {
	for err, want := range map[error]bool{
		errRL: true, err5xx: true, errTO: true, errConn: true,
		err4xx: false, context.Canceled: false, errors.New("other"): false,
		fmt.Errorf("wrapped: %w", err5xx): true,
	} {
		if Retryable(err) != want {
			t.Errorf("Retryable(%v) = %v, want %v", err, !want, want)
		}
	}
}

func TestDelay(t *testing.T) {
	half := func() float64 { return 0.5 }
	p := DefaultPolicy()
	p.Rand = half
	tests := []struct {
		name  string
		p     Policy
		retry int
		err   error
		want  time.Duration
	}{
		{"hint plus jitter", p, 1, errRL, 2*time.Second + 250*time.Millisecond},
		{"429 without hint backs off", p, 1, errRLNoHint, 250 * time.Millisecond},
		{"full jitter retry 1", p, 1, err5xx, 250 * time.Millisecond}, // 0.5 * 500ms
		{"full jitter retry 2", p, 2, err5xx, 500 * time.Millisecond}, // 0.5 * 1s
		{"capped", p, 10, err5xx, 4 * time.Second},                    // 0.5 * 8s cap
		{"naive ignores hint", NaivePolicy(), 1, errRL, time.Second},
		{"naive is fixed", NaivePolicy(), 2, err5xx, time.Second},
	}
	for _, tt := range tests {
		if got := tt.p.Delay(tt.retry, tt.err); got != tt.want {
			t.Errorf("%s: %v, want %v", tt.name, got, tt.want)
		}
	}
}

// fakeTime lets Do's sleeps advance a clock instantly.
func fakeTime(p *Policy) *[]time.Duration {
	now := time.Unix(1_800_000_000, 0)
	var slept []time.Duration
	p.Now = func() time.Time { return now }
	p.Sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); now = now.Add(d); return nil }
	p.Rand = func() float64 { return 0.5 }
	return &slept
}

// script returns a call that fails with errs in order, then succeeds.
func script(errs ...error) (func(context.Context) (string, error), *int) {
	calls := 0
	return func(context.Context) (string, error) {
		calls++
		if calls <= len(errs) {
			return "", errs[calls-1]
		}
		return "answer", nil
	}, &calls
}

func TestDoRetriesThenSucceeds(t *testing.T) {
	p := DefaultPolicy()
	slept := fakeTime(&p)
	call, calls := script(errRL, err5xx)
	v, st, err := Do(context.Background(), p, call)
	if err != nil || v != "answer" || *calls != 3 {
		t.Fatalf("v=%q err=%v calls=%d", v, err, *calls)
	}
	if st.Retries != 2 || st.RateLimited != 1 || st.Backoff != 2250*time.Millisecond+500*time.Millisecond {
		t.Errorf("stats %+v, slept %v", st, *slept)
	}
}

func TestDoGivesUpAfterMaxRetries(t *testing.T) {
	p := DefaultPolicy()
	fakeTime(&p)
	call, calls := script(err5xx, err5xx, err5xx, err5xx)
	_, st, err := Do(context.Background(), p, call)
	if !errors.Is(err, err5xx) || *calls != 3 || st.Retries != 2 {
		t.Errorf("err=%v calls=%d stats=%+v", err, *calls, st)
	}
}

func TestDoNeverRetriesClientErrors(t *testing.T) {
	p := DefaultPolicy()
	fakeTime(&p)
	call, calls := script(err4xx)
	if _, _, err := Do(context.Background(), p, call); !errors.Is(err, err4xx) || *calls != 1 {
		t.Errorf("err=%v calls=%d", err, *calls)
	}
}

func TestDoStopsWhenDeadlineWouldPass(t *testing.T) {
	p := DefaultPolicy()
	slept := fakeTime(&p)
	// 2s left: a 2.25s hinted wait cannot fit, so no retry is attempted.
	ctx, cancel := context.WithDeadline(context.Background(), p.Now().Add(2*time.Second))
	defer cancel()
	call, calls := script(errRL)
	_, st, err := Do(ctx, p, call)
	if !errors.Is(err, errRL) || *calls != 1 || len(*slept) != 0 || st.RateLimited != 1 {
		t.Errorf("err=%v calls=%d slept=%v", err, *calls, *slept)
	}
}

func TestDoStopsIfCallerCancelsDuringBackoff(t *testing.T) {
	p := DefaultPolicy()
	p.Sleep = func(context.Context, time.Duration) error { return context.Canceled }
	call, calls := script(err5xx)
	if _, _, err := Do(context.Background(), p, call); !errors.Is(err, err5xx) || *calls != 1 {
		t.Errorf("want the call's error after a cancelled backoff, got %v (calls=%d)", err, *calls)
	}
}
