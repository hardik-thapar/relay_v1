package llm

import (
	"fmt"
	"net/http"
	"time"
)

// The retry policy decides by error type, so each failure mode gets its own.

// RateLimitError is a 429. RetryAfter is the provider's hint (0 if absent).
type RateLimitError struct {
	RetryAfter time.Duration
	Limits     RateLimits
	Header     http.Header
	Body       string
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("rate limited (retry after %s): %s", e.RetryAfter, e.Body)
}

// ServerError is a 5xx, or Groq's 498 (flex capacity), or an error event
// sent inside an otherwise-200 stream.
type ServerError struct {
	Status int
	Body   string
}

func (e *ServerError) Error() string { return fmt.Sprintf("server error %d: %s", e.Status, e.Body) }

// ClientError is any other 4xx: our request is wrong, retrying cannot help.
type ClientError struct {
	Status int
	Body   string
}

func (e *ClientError) Error() string { return fmt.Sprintf("client error %d: %s", e.Status, e.Body) }

// TimeoutError means a deadline passed. Phase is "first_token" if nothing
// had arrived yet, "total" if the answer had started streaming.
type TimeoutError struct {
	Phase string
}

func (e *TimeoutError) Error() string { return "timeout before " + e.Phase }

// ConnError is a dropped or broken connection: dial failure, reset, or a
// stream that ended without its terminator. Partial holds text received.
type ConnError struct {
	Partial string
	Err     error
}

func (e *ConnError) Error() string { return "connection: " + e.Err.Error() }
func (e *ConnError) Unwrap() error { return e.Err }
