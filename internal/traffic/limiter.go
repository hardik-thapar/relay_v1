// Package traffic decides when model calls may run: the limiter keeps us
// inside the provider's limits, the retry policy handles failures, and
// admission queues requests or steps them down the degradation ladder.
package traffic

import (
	"sync"
	"time"

	"relay/internal/bucket"
	"relay/internal/llm"
)

// Limits are the provider's, as measured by the probe (Table 1).
type Limits struct {
	RPM, TPM int // per minute
	RPD, TPD int // per day
}

// GroqFree is what the probe found for openai/gpt-oss-20b on the free
// tier. TPD is from the docs only (not measured).
var GroqFree = Limits{RPM: 30, TPM: 8000, RPD: 1000, TPD: 200000}

// Which limit is in the way. Admission treats them differently: a minute
// limit means "wait or give less", a day limit means "budget is gone".
const (
	ByMinute = "minute"
	ByDay    = "day"
	ByPause  = "retry-after" // the provider told us to back off
)

// Limiter is our own model of the provider's buckets. We trust it over
// the provider: the probe showed Groq letting 34 of 35 burst calls past a
// 30 RPM limit (DECISIONS D6). Safe for concurrent use.
type Limiter struct {
	mu          sync.Mutex
	now         func() time.Time
	rpm, tpm    *bucket.Bucket
	rpd, tpd    *bucket.Bucket
	pausedUntil time.Time
}

// NewLimiter starts with full buckets. now is the clock (nil = real time).
func NewLimiter(l Limits, now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	t := now()
	return &Limiter{
		now: now,
		rpm: bucket.New(l.RPM, time.Minute, -1, t),
		tpm: bucket.New(l.TPM, time.Minute, -1, t),
		rpd: bucket.New(l.RPD, 24*time.Hour, -1, t),
		tpd: bucket.New(l.TPD, 24*time.Hour, -1, t),
	}
}

// Wait estimates how long until `requests` calls totalling `tokens` will
// all have started, and which limit is the reason. It reserves nothing;
// admission passes in a whole queue, which may exceed a bucket and is
// then served over several refills.
func (l *Limiter) Wait(requests, tokens int) (time.Duration, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.wait(float64(requests), float64(tokens), (*bucket.Bucket).TimeFor)
}

// Fits reports whether one call of `tokens` could ever be admitted.
func (l *Limiter) Fits(tokens int) bool {
	return l.tpm.Fits(float64(tokens)) && l.tpd.Fits(float64(tokens))
}

// wait takes the longest wait across all limits. waitFn is TimeFor for a
// queue estimate, or Wait for one call (which knows "never fits").
func (l *Limiter) wait(requests, tokens float64, waitFn func(*bucket.Bucket, float64, time.Time) time.Duration) (time.Duration, string) {
	now := l.now()
	var longest time.Duration
	why := ""
	consider := func(d time.Duration, reason string) {
		if d > longest {
			longest, why = d, reason
		}
	}
	consider(l.pausedUntil.Sub(now), ByPause)
	consider(waitFn(l.rpm, requests, now), ByMinute)
	consider(waitFn(l.tpm, tokens, now), ByMinute)
	consider(waitFn(l.rpd, requests, now), ByDay)
	consider(waitFn(l.tpd, tokens, now), ByDay)
	return longest, why
}

// TryReserve takes room for one call of about `tokens` tokens if every
// limit allows it right now. Otherwise it reserves nothing and returns
// the wait and its reason. tokens should be input + max output: we
// over-reserve and give back the rest in Finish.
func (l *Limiter) TryReserve(tokens int) (*Reservation, time.Duration, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if wait, why := l.wait(1, float64(tokens), (*bucket.Bucket).Wait); wait > 0 {
		return nil, wait, why
	}
	now := l.now()
	l.rpm.Charge(1, now)
	l.rpd.Charge(1, now)
	l.tpm.Charge(float64(tokens), now)
	l.tpd.Charge(float64(tokens), now)
	return &Reservation{l: l, tokens: tokens}, 0, ""
}

// Reservation is room taken for one call. Exactly one of Finish or
// Cancel should be called; extra calls do nothing.
type Reservation struct {
	l      *Limiter
	tokens int
	done   bool
}

// Finish settles the reservation with the tokens the provider reported:
// unused tokens go back, an overrun is charged. The request still counts.
func (r *Reservation) Finish(actualTokens int) {
	r.settle(float64(r.tokens-actualTokens), 0)
}

// Cancel returns everything: the call was never sent.
func (r *Reservation) Cancel() {
	r.settle(float64(r.tokens), 1)
}

func (r *Reservation) settle(tokensBack, requestsBack float64) {
	l := r.l
	l.mu.Lock()
	defer l.mu.Unlock()
	if r.done {
		return
	}
	r.done = true
	now := l.now()
	l.tpm.Charge(-tokensBack, now)
	l.tpd.Charge(-tokensBack, now)
	l.rpm.Charge(-requestsBack, now)
	l.rpd.Charge(-requestsBack, now)
}

// Observe corrects our count from the provider's headers (good-to-have
// #1). Groq's *-requests headers are per day, *-tokens per minute. We
// only ever lower our count: inside a burst the headers disagree with
// each other, so the lowest is the safe one to believe (DECISIONS D6).
func (l *Limiter) Observe(h llm.RateLimits) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if h.RemainingRequests >= 0 {
		l.rpd.LowerTo(float64(h.RemainingRequests), now)
	}
	if h.RemainingTokens >= 0 {
		l.tpm.LowerTo(float64(h.RemainingTokens), now)
	}
}

// PauseFor stops all new calls for d, after the provider answered 429
// with a retry hint. Extending is allowed, shortening is not.
func (l *Limiter) PauseFor(d time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if until := l.now().Add(d); until.After(l.pausedUntil) {
		l.pausedUntil = until
	}
}
