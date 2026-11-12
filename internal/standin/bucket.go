package standin

import (
	"math"
	"time"
)

// bucket is the provider-side limit: capacity refilled evenly over a
// period, which is how Groq's TPM and RPD behaved in the probe.
// A nil bucket means "no limit". Callers hold the server lock.
type bucket struct {
	capacity float64
	perSec   float64 // refill rate
	level    float64
	last     time.Time
}

// newBucket returns nil when capacity <= 0. start < 0 means full.
func newBucket(capacity int, period time.Duration, start int, now time.Time) *bucket {
	if capacity <= 0 {
		return nil
	}
	level := float64(capacity)
	if start >= 0 {
		level = float64(start)
	}
	return &bucket{capacity: float64(capacity), perSec: float64(capacity) / period.Seconds(), level: level, last: now}
}

func (b *bucket) refill(now time.Time) {
	if dt := now.Sub(b.last).Seconds(); dt > 0 {
		b.level = math.Min(b.capacity, b.level+dt*b.perSec)
		b.last = now
	}
}

// wait is how long until n units are available (0 = available now).
func (b *bucket) wait(n float64, now time.Time) time.Duration {
	if b == nil {
		return 0
	}
	b.refill(now)
	if b.level >= n {
		return 0
	}
	return time.Duration((n - b.level) / b.perSec * float64(time.Second))
}

// charge removes n units; the level may go negative (output tokens are
// only known after the fact).
func (b *bucket) charge(n float64, now time.Time) {
	if b == nil {
		return
	}
	b.refill(now)
	b.level -= n
}

// remaining is the whole units left, floored at 0.
func (b *bucket) remaining(now time.Time) int {
	b.refill(now)
	return int(math.Max(0, math.Floor(b.level)))
}

// untilFull is what Groq reports as x-ratelimit-reset-*.
func (b *bucket) untilFull(now time.Time) time.Duration {
	b.refill(now)
	return time.Duration((b.capacity - b.level) / b.perSec * float64(time.Second))
}
