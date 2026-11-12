// Package standin is a fake model provider we control: slow, slower under
// load, failing on purpose, refusing at limits, streaming in pieces.
// It speaks the same wire format as Groq so llm.HTTPClient cannot tell.
package standin

import (
	"encoding/json"
	"os"
)

// Config is every knob of the stand-in. Default() fills it from the probe;
// a JSON file can override any field for a scenario.
type Config struct {
	Source string // which probe run the numbers came from

	// Latency (milliseconds).
	FirstTokenMS      []float64 // idle first-token samples, resampled per call
	InputMSPerToken   float64   // extra first-token delay per prompt token
	OutputMSPerToken  float64   // streaming speed after the first token
	LoadMSPerInFlight float64   // extra first-token delay per other request in flight
	RefusalMS         float64   // how long a 429 takes to come back

	// Token accounting (no real tokenizer: chars / CharsPerToken).
	PromptOverheadTokens int
	CharsPerToken        float64
	ReasoningTokens      int // hidden tokens spent before any content

	// Failures, as probabilities per request. Not calibrated: the probe
	// saw none in 61 calls, so scenarios set these.
	ServerErrorRate float64
	DropRate        float64 // connection cut partway through the stream

	// Limits. Zero disables a limit. Start* set the daily buckets' initial
	// level (-1 = full), for the "daily budget exhausted" scenario.
	RPM, TPM, RPD, TPD int
	MaxInFlight        int
	StartRPD, StartTPD int

	Seed uint64
}

// Default returns the stand-in calibrated from probe run 20261112T054119Z
// (results/probe/README.md).
func Default() Config {
	return Config{
		Source:            "probe 20261112T054119Z",
		FirstTokenMS:      []float64{220, 285, 317, 348, 358, 405, 408, 416, 420, 432},
		InputMSPerToken:   0.05,
		OutputMSPerToken:  0.8,
		LoadMSPerInFlight: 24,
		RefusalMS:         130,

		PromptOverheadTokens: 72,
		CharsPerToken:        5,
		ReasoningTokens:      20,

		RPM: 30, TPM: 8000, RPD: 1000, TPD: 200000,
		StartRPD: -1, StartTPD: -1,
		Seed: 1,
	}
}

// Load reads a JSON file of overrides on top of Default().
func Load(path string) (Config, error) {
	cfg := Default()
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	err = json.Unmarshal(b, &cfg)
	return cfg, err
}
