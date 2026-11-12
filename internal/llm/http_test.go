package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// sse writes one SSE data line and flushes it.
func sse(w http.ResponseWriter, data string) {
	fmt.Fprintf(w, "data: %s\n\n", data)
	w.(http.Flusher).Flush()
}

func chunk(content string) string {
	return fmt.Sprintf(`{"choices":[{"delta":{"content":%q}}]}`, content)
}

func newServer(t *testing.T, h http.HandlerFunc) *HTTPClient {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return NewHTTP(srv.URL, "test-key")
}

func TestCompleteStreamsAndCountsTokens(t *testing.T) {
	var got wireRequest
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("bad path or auth: %s %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("x-ratelimit-remaining-tokens", "7000")
		w.Header().Set("x-ratelimit-reset-requests", "2m59.56s")
		sse(w, `{"choices":[{"delta":{"role":"assistant"}}]}`) // no content: not a first token
		sse(w, chunk("Hello "))
		sse(w, chunk("world"))
		sse(w, `{"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":5}}`)
		sse(w, "[DONE]")
	})

	resp, err := c.Complete(context.Background(), Request{Model: "m", MaxTokens: 50, Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "Hello world" || resp.TokensIn != 12 || resp.TokensOut != 5 {
		t.Errorf("got text=%q in=%d out=%d", resp.Text, resp.TokensIn, resp.TokensOut)
	}
	if !got.Stream || got.Model != "m" || got.MaxTokens != 50 || got.StreamOptions == nil {
		t.Errorf("request body not as expected: %+v", got)
	}
	if resp.FirstToken.Before(resp.Sent) || resp.Done.Before(resp.FirstToken) {
		t.Errorf("timings out of order: sent=%v first=%v done=%v", resp.Sent, resp.FirstToken, resp.Done)
	}
	if resp.Limits.RemainingTokens != 7000 || resp.Limits.ResetRequests != 2*time.Minute+59560*time.Millisecond {
		t.Errorf("limits: %+v", resp.Limits)
	}
}

func TestCompleteReadsGroqUsageBlock(t *testing.T) {
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		sse(w, chunk("x"))
		sse(w, `{"choices":[],"x_groq":{"usage":{"prompt_tokens":3,"completion_tokens":1}}}`)
		sse(w, "[DONE]")
	})
	resp, err := c.Complete(context.Background(), Request{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.TokensIn != 3 || resp.TokensOut != 1 {
		t.Errorf("in=%d out=%d", resp.TokensIn, resp.TokensOut)
	}
}

func TestCompleteMapsStatusCodes(t *testing.T) {
	tests := []struct {
		status int
		check  func(error) bool
	}{
		{429, func(e error) bool {
			var rl *RateLimitError
			return errors.As(e, &rl) && rl.RetryAfter == 2500*time.Millisecond && strings.Contains(rl.Body, "limit")
		}},
		{500, func(e error) bool { var se *ServerError; return errors.As(e, &se) && se.Status == 500 }},
		{503, func(e error) bool { var se *ServerError; return errors.As(e, &se) }},
		{498, func(e error) bool { var se *ServerError; return errors.As(e, &se) }},
		{400, func(e error) bool { var ce *ClientError; return errors.As(e, &ce) && ce.Status == 400 }},
		{401, func(e error) bool { var ce *ClientError; return errors.As(e, &ce) }},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.status), func(t *testing.T) {
			c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("retry-after", "2.5")
				w.WriteHeader(tt.status)
				io.WriteString(w, `{"error":{"message":"rate limit reached"}}`)
			})
			_, err := c.Complete(context.Background(), Request{})
			if !tt.check(err) {
				t.Errorf("status %d gave %T: %v", tt.status, err, err)
			}
		})
	}
}

func TestCompleteErrorEventInStream(t *testing.T) {
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		sse(w, chunk("partial"))
		sse(w, `{"error":{"message":"overloaded"}}`)
	})
	_, err := c.Complete(context.Background(), Request{})
	var se *ServerError
	if !errors.As(err, &se) || se.Body != "overloaded" {
		t.Errorf("got %T: %v", err, err)
	}
}

func TestCompleteFirstTokenTimeout(t *testing.T) {
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done() // never send content
	})
	_, err := c.Complete(context.Background(), Request{FirstTokenTimeout: 30 * time.Millisecond})
	var te *TimeoutError
	if !errors.As(err, &te) || te.Phase != "first_token" {
		t.Errorf("got %T: %v", err, err)
	}
}

func TestCompleteFirstTokenTimerStopsOnceStreaming(t *testing.T) {
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		sse(w, chunk("a"))
		time.Sleep(60 * time.Millisecond) // longer than the first-token timeout
		sse(w, chunk("b"))
		sse(w, "[DONE]")
	})
	resp, err := c.Complete(context.Background(), Request{FirstTokenTimeout: 30 * time.Millisecond})
	if err != nil || resp.Text != "ab" {
		t.Errorf("got %v, %v", resp, err)
	}
}

func TestCompleteTotalTimeoutAfterFirstToken(t *testing.T) {
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		sse(w, chunk("a"))
		<-r.Context().Done()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := c.Complete(ctx, Request{})
	var te *TimeoutError
	if !errors.As(err, &te) || te.Phase != "total" {
		t.Errorf("got %T: %v", err, err)
	}
}

func TestCompleteCallerCancelIsNotTimeout(t *testing.T) {
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		// The server only notices a client disconnect once the body is read.
		io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	_, err := c.Complete(ctx, Request{})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("got %T: %v", err, err)
	}
}

func TestCompleteDroppedConnection(t *testing.T) {
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		sse(w, chunk("half an ans"))
		panic(http.ErrAbortHandler) // server cuts the connection mid-stream
	})
	_, err := c.Complete(context.Background(), Request{})
	var ce *ConnError
	if !errors.As(err, &ce) || ce.Partial != "half an ans" {
		t.Errorf("got %T: %v", err, err)
	}
}

func TestCompleteStreamEndsWithoutDone(t *testing.T) {
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		sse(w, chunk("x")) // handler returns cleanly, but no [DONE]
	})
	_, err := c.Complete(context.Background(), Request{})
	var ce *ConnError
	if !errors.As(err, &ce) || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("got %T: %v", err, err)
	}
}

func TestCompleteUnreachableServer(t *testing.T) {
	c := NewHTTP("http://127.0.0.1:1", "")
	resp, err := c.Complete(context.Background(), Request{})
	var ce *ConnError
	if !errors.As(err, &ce) {
		t.Errorf("got %T: %v", err, err)
	}
	// No headers arrived, so nothing is known about the limits. A zero
	// value here once made the limiter believe the daily budget was gone.
	if resp.Limits != UnknownLimits {
		t.Errorf("limits without headers = %+v, want UnknownLimits", resp.Limits)
	}
}

func TestParseDur(t *testing.T) {
	tests := map[string]time.Duration{
		"":         0,
		"2":        2 * time.Second,
		"0.5":      500 * time.Millisecond,
		"7.66s":    7660 * time.Millisecond,
		"2m59.56s": 2*time.Minute + 59560*time.Millisecond,
		"garbage":  0,
	}
	for in, want := range tests {
		if got := parseDur(in); got != want {
			t.Errorf("parseDur(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestParseLimitsMissingHeaders(t *testing.T) {
	l := parseLimits(http.Header{})
	if l.LimitRequests != -1 || l.RemainingTokens != -1 || l.RetryAfter != 0 {
		t.Errorf("missing headers should give -1/0, got %+v", l)
	}
}
