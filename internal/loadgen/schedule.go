// Package loadgen drives the pipeline with an open-loop schedule and
// records one results.csv row per request (DESIGN §6).
package loadgen

import (
	"math/rand/v2"
	"time"
)

// Phase is a stretch of time with a constant average arrival rate.
type Phase struct {
	PerMinute float64
	For       time.Duration
}

// Arrivals returns start offsets for every request, phase after phase.
// Gaps are exponential (a Poisson process): real traffic is bursty even
// at a steady average, and evenly spaced arrivals would flatter us. The
// seed makes a run repeatable.
func Arrivals(phases []Phase, seed uint64) []time.Duration {
	rng := rand.New(rand.NewPCG(seed, seed))
	var out []time.Duration
	var phaseStart time.Duration
	for _, ph := range phases {
		if ph.PerMinute > 0 {
			meanGap := float64(time.Minute) / ph.PerMinute
			t := time.Duration(rng.ExpFloat64() * meanGap)
			for t < ph.For {
				out = append(out, phaseStart+t)
				t += time.Duration(rng.ExpFloat64() * meanGap)
			}
		}
		phaseStart += ph.For
	}
	return out
}

// Burst is n requests at one instant, after `at`: the "something broke,
// everyone asks at once" shape the brief describes.
func Burst(at time.Duration, n int) []time.Duration {
	out := make([]time.Duration, n)
	for i := range out {
		out[i] = at
	}
	return out
}
