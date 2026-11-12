package main

import (
	"time"

	"relay/internal/loadgen"
	"relay/internal/standin"
	"relay/internal/traffic"
)

// scenario is one load shape plus the world it runs in.
//
// Rates come from the probe: TPM binds first, and at ~1.9K tokens per
// answer 8K TPM serves about 4 answers a minute. "Steady" sits under
// that, "overload" is 3x over it.
type scenario struct {
	arrivals func(seed uint64) []time.Duration
	provider func(*standin.Config) // how the stand-in behaves (ignored for real runs)
	limits   traffic.Limits        // what OUR limiter believes
	patience time.Duration         // how long a person waits before giving up
	eval     bool                  // closed loop over the questions, graded
	real     bool                  // allowed against real Groq (budget guard)
}

const patience = 20 * time.Second // DESIGN §5: TBD; starting guess, tested by the runs

var scenarios = map[string]scenario{
	// Eval: each question once, one at a time, graded. Generous patience
	// so the limiter may queue rather than refuse.
	"eval": {eval: true, real: true, limits: traffic.GroqFree, patience: 2 * time.Minute, provider: func(*standin.Config) {}},

	// Under capacity: 3 a minute for 5 minutes.
	"steady": {
		arrivals: func(s uint64) []time.Duration {
			return loadgen.Arrivals([]loadgen.Phase{{PerMinute: 3, For: 5 * time.Minute}}, s)
		},
		provider: func(*standin.Config) {}, limits: traffic.GroqFree, patience: patience, real: true,
	},

	// Steady background, then 20 people ask at the same instant at 1:00.
	"burst": {
		arrivals: func(s uint64) []time.Duration {
			return append(loadgen.Arrivals([]loadgen.Phase{{PerMinute: 3, For: 4 * time.Minute}}, s), loadgen.Burst(time.Minute, 20)...)
		},
		provider: func(*standin.Config) {}, limits: traffic.GroqFree, patience: patience,
	},

	// 3x capacity for 4 minutes, with some provider failures to retry.
	"overload": {
		arrivals: func(s uint64) []time.Duration {
			return loadgen.Arrivals([]loadgen.Phase{{PerMinute: 12, For: 4 * time.Minute}}, s)
		},
		provider: func(c *standin.Config) { c.ServerErrorRate, c.DropRate = 0.02, 0.01 },
		limits:   traffic.GroqFree, patience: patience,
	},

	// The provider halves its per-minute limits without telling us: our
	// limiter still believes 30 RPM / 8K TPM and must learn from headers
	// and 429s. The brief: limits "can change without notice".
	"halved": {
		arrivals: func(s uint64) []time.Duration {
			return loadgen.Arrivals([]loadgen.Phase{{PerMinute: 3, For: 4 * time.Minute}}, s)
		},
		provider: func(c *standin.Config) { c.RPM, c.TPM = 15, 4000 },
		limits:   traffic.GroqFree, patience: patience,
	},

	// The day's requests are gone at the provider; one trickles back every
	// 86.4 s. Our limiter starts unaware and learns from the first reply.
	"daily-exhausted": {
		arrivals: func(s uint64) []time.Duration {
			return loadgen.Arrivals([]loadgen.Phase{{PerMinute: 3, For: 3 * time.Minute}}, s)
		},
		provider: func(c *standin.Config) { c.StartRPD = 0 },
		limits:   traffic.GroqFree, patience: patience,
	},

	// Plumbing check only: a few seconds of load, for tests and smoke runs.
	"smoke": {
		arrivals: func(s uint64) []time.Duration {
			return loadgen.Arrivals([]loadgen.Phase{{PerMinute: 300, For: 2 * time.Second}}, s)
		},
		provider: func(*standin.Config) {}, limits: traffic.GroqFree, patience: patience,
	},
}
