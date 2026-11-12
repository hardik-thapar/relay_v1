// Package llm is the model client. One HTTP implementation talks to any
// OpenAI-compatible streaming endpoint, so Groq and the stand-in are
// indistinguishable to the pipeline (DESIGN.md §4).
package llm

import (
	"context"
	"net/http"
	"time"
)

// Message is one chat turn.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Request is everything the pipeline controls about a model call.
type Request struct {
	Model           string
	Messages        []Message
	MaxTokens       int
	Temperature     float64
	ReasoningEffort string // "low", "medium", "high"; empty = provider default

	// FirstTokenTimeout cancels the call if no content arrives in time.
	// Zero means no separate first-token deadline (ctx still applies).
	FirstTokenTimeout time.Duration
}

// Response is a finished call. Times are wall-clock so the recorder can
// split latency into waiting (before Sent) and working (Sent..Done).
type Response struct {
	Text      string
	TokensIn  int
	TokensOut int // includes reasoning tokens: they count toward limits

	Sent       time.Time // request written
	FirstToken time.Time // first non-empty content piece
	Done       time.Time // stream finished

	Limits RateLimits
	Header http.Header // raw response headers, kept for the probe
}

// RateLimits holds the provider's view of our remaining budget.
// Per Groq docs: *Requests* fields refer to the per-day limit, *Tokens*
// fields to the per-minute limit. -1 / 0 mean the header was absent.
type RateLimits struct {
	LimitRequests     int
	RemainingRequests int
	ResetRequests     time.Duration
	LimitTokens       int
	RemainingTokens   int
	ResetTokens       time.Duration
	RetryAfter        time.Duration // only set on 429
}

// UnknownLimits is RateLimits with no information: what a response that
// never got headers carries. Never use the zero value to mean "unknown".
var UnknownLimits = RateLimits{LimitRequests: -1, RemainingRequests: -1, LimitTokens: -1, RemainingTokens: -1}

// Client is the one interface the pipeline sees. On error the returned
// Response (possibly nil) still carries whatever timings and headers were
// gathered, so a failed call can be placed on the timeline.
type Client interface {
	Complete(ctx context.Context, req Request) (*Response, error)
}
