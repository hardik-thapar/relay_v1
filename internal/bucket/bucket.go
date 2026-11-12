// Package bucket is a token bucket: capacity refilled evenly over a period.
// Groq's per-minute and per-day limits behaved this way in the probe
// (DECISIONS D6), so both the stand-in (provider side) and the limiter
// (our side) use it. A Bucket is not safe for concurrent use; callers lock.
package bucket

import (
	"math"
	"time"
)

type Bucket struct {
	capacity float64
	perSec   float64 // refill rate
	level    float64 // may go negative after a charge we could not predict
	last     time.Time
}

// New returns nil when capacity <= 0; a nil *Bucket never limits.
// start < 0 means the bucket starts full.
func New(capacity int, period time.Duration, start int, now time.Time) *Bucket {
	if capacity <= 0 {
		return nil
	}
	level := float64(capacity)
	if start >= 0 {
		level = float64(start)
	}
	return &Bucket{capacity: float64(capacity), perSec: float64(capacity) / period.Seconds(), level: level, last: now}
}

func (b *Bucket) refill(now time.Time) {
	if dt := now.Sub(b.last).Seconds(); dt > 0 {
		b.level = math.Min(b.capacity, b.level+dt*b.perSec)
		b.last = now
	}
}

// Wait is how long until n units are available (0 = now). If n exceeds
// the capacity it can never fit, and Wait returns Never.
func (b *Bucket) Wait(n float64, now time.Time) time.Duration {
	if b == nil {
		return 0
	}
	if n > b.capacity {
		return Never
	}
	b.refill(now)
	if b.level >= n {
		return 0
	}
	return time.Duration((n - b.level) / b.perSec * float64(time.Second))
}

// Never is the wait for a request larger than the whole bucket.
const Never = time.Duration(math.MaxInt64)

// Charge removes n units (negative n gives units back, capped at capacity).
// The level may go below zero: output tokens are only known afterwards.
func (b *Bucket) Charge(n float64, now time.Time) {
	if b == nil {
		return
	}
	b.refill(now)
	b.level = math.Min(b.capacity, b.level-n)
}

// LowerTo caps the level at v: used to correct our count from the
// provider's headers. It never raises the level.
func (b *Bucket) LowerTo(v float64, now time.Time) {
	if b == nil {
		return
	}
	b.refill(now)
	b.level = math.Min(b.level, v)
}

// Remaining is the whole units left, floored at 0.
func (b *Bucket) Remaining(now time.Time) int {
	b.refill(now)
	return int(math.Max(0, math.Floor(b.level)))
}

// UntilFull is what Groq reports as x-ratelimit-reset-*.
func (b *Bucket) UntilFull(now time.Time) time.Duration {
	b.refill(now)
	return time.Duration((b.capacity - b.level) / b.perSec * float64(time.Second))
}

// Capacity is the bucket size.
func (b *Bucket) Capacity() int { return int(b.capacity) }
