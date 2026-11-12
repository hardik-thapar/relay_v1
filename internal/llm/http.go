package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// HTTPClient calls an OpenAI-compatible /chat/completions endpoint with
// streaming. Groq and the stand-in differ only in BaseURL and APIKey.
type HTTPClient struct {
	BaseURL string // e.g. https://api.groq.com/openai/v1
	APIKey  string
	HTTP    *http.Client
}

// NewHTTP returns a client with no client-level timeout: deadlines come
// from the caller's ctx and Request.FirstTokenTimeout, so we can tell
// which one fired.
func NewHTTP(baseURL, apiKey string) *HTTPClient {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConnsPerHost = 64 // the load generator runs many calls at once
	return &HTTPClient{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		HTTP:    &http.Client{Transport: t},
	}
}

var errFirstTokenTimeout = errors.New("first token timeout")

// Complete sends one streamed request and returns the full answer.
func (c *HTTPClient) Complete(ctx context.Context, req Request) (*Response, error) {
	body, err := json.Marshal(wireRequest{
		Model:           req.Model,
		Messages:        req.Messages,
		MaxTokens:       req.MaxTokens,
		Temperature:     req.Temperature,
		ReasoningEffort: req.ReasoningEffort,
		Stream:          true,
		StreamOptions:   &streamOptions{IncludeUsage: true},
	})
	if err != nil {
		return nil, err
	}

	// A cause-carrying ctx lets classify tell "first token too slow" apart
	// from "whole answer too slow" and from "caller gave up".
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	stopFirstTimer := func() {}
	if req.FirstTokenTimeout > 0 {
		t := time.AfterFunc(req.FirstTokenTimeout, func() { cancel(errFirstTokenTimeout) })
		stopFirstTimer = func() { t.Stop() }
		defer t.Stop()
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if c.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	// Limits stay "unknown" until headers arrive: a zero value would read
	// as "0 requests left today" to anyone correcting counts from them.
	resp := &Response{Sent: time.Now(), Limits: UnknownLimits}
	hr, err := c.HTTP.Do(httpReq)
	if err != nil {
		return resp, classify(ctx, err, resp)
	}
	defer hr.Body.Close()
	resp.Header = hr.Header
	resp.Limits = parseLimits(hr.Header)

	if hr.StatusCode != http.StatusOK {
		return resp, statusError(hr, resp.Limits)
	}
	if err := readStream(hr.Body, resp, stopFirstTimer); err != nil {
		return resp, classify(ctx, err, resp)
	}
	resp.Done = time.Now()
	return resp, nil
}

// readStream consumes SSE lines ("data: {...}" ... "data: [DONE]"),
// accumulating content into resp.Text and usage into resp.Tokens*.
// A stream that ends without [DONE] is a dropped connection.
func readStream(r io.Reader, resp *Response, onFirst func()) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	var text strings.Builder
	defer func() { resp.Text = text.String() }()

	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue // blank separators, comments, "event:" lines
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			return nil
		}
		var ch wireChunk
		if err := json.Unmarshal([]byte(data), &ch); err != nil {
			return &ServerError{Status: http.StatusOK, Body: "malformed chunk: " + data}
		}
		if ch.Error != nil {
			return &ServerError{Status: http.StatusOK, Body: ch.Error.Message}
		}
		for _, choice := range ch.Choices {
			if choice.Delta.Content == "" {
				continue // role-only or reasoning-only pieces
			}
			if resp.FirstToken.IsZero() {
				resp.FirstToken = time.Now()
				onFirst()
			}
			text.WriteString(choice.Delta.Content)
		}
		if u := ch.usage(); u != nil {
			resp.TokensIn = u.PromptTokens
			resp.TokensOut = u.CompletionTokens
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return io.ErrUnexpectedEOF
}

// classify turns a transport or stream failure into a typed error.
func classify(ctx context.Context, err error, resp *Response) error {
	if ctx.Err() != nil {
		cause := context.Cause(ctx)
		switch {
		case errors.Is(cause, errFirstTokenTimeout):
			return &TimeoutError{Phase: "first_token"}
		case errors.Is(cause, context.DeadlineExceeded):
			if resp.FirstToken.IsZero() {
				return &TimeoutError{Phase: "first_token"}
			}
			return &TimeoutError{Phase: "total"}
		default:
			return ctx.Err() // the caller cancelled; not ours to classify
		}
	}
	var se *ServerError
	if errors.As(err, &se) {
		return se
	}
	return &ConnError{Partial: resp.Text, Err: err}
}

// statusError maps a non-200 response to a typed error.
func statusError(hr *http.Response, limits RateLimits) error {
	b, _ := io.ReadAll(io.LimitReader(hr.Body, 64<<10))
	body := string(b)
	switch code := hr.StatusCode; {
	case code == http.StatusTooManyRequests:
		return &RateLimitError{RetryAfter: limits.RetryAfter, Limits: limits, Header: hr.Header, Body: body}
	case code >= 500 || code == 498: // 498 = Groq flex tier at capacity
		return &ServerError{Status: code, Body: body}
	default:
		return &ClientError{Status: code, Body: body}
	}
}

// parseLimits reads Groq's rate-limit headers. Missing counts are -1,
// missing durations are 0.
func parseLimits(h http.Header) RateLimits {
	return RateLimits{
		LimitRequests:     atoi(h.Get("x-ratelimit-limit-requests")),
		RemainingRequests: atoi(h.Get("x-ratelimit-remaining-requests")),
		ResetRequests:     parseDur(h.Get("x-ratelimit-reset-requests")),
		LimitTokens:       atoi(h.Get("x-ratelimit-limit-tokens")),
		RemainingTokens:   atoi(h.Get("x-ratelimit-remaining-tokens")),
		ResetTokens:       parseDur(h.Get("x-ratelimit-reset-tokens")),
		RetryAfter:        parseDur(h.Get("retry-after")),
	}
}

func atoi(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return -1
	}
	return n
}

// parseDur accepts plain seconds ("2", "0.5") or Go durations ("2m59.56s").
func parseDur(s string) time.Duration {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return time.Duration(f * float64(time.Second))
	}
	if d, err := time.ParseDuration(s); err == nil {
		return d
	}
	return 0
}

// Wire formats (OpenAI-compatible).

type wireRequest struct {
	Model           string         `json:"model"`
	Messages        []Message      `json:"messages"`
	MaxTokens       int            `json:"max_completion_tokens,omitempty"`
	Temperature     float64        `json:"temperature"`
	ReasoningEffort string         `json:"reasoning_effort,omitempty"`
	Stream          bool           `json:"stream"`
	StreamOptions   *streamOptions `json:"stream_options,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type wireUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

type wireChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *wireUsage `json:"usage"`
	XGroq *struct {
		Usage *wireUsage `json:"usage"`
	} `json:"x_groq"` // Groq also reports usage here on the last chunk
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// usage returns whichever usage block the provider sent, if any.
func (c *wireChunk) usage() *wireUsage {
	if c.Usage != nil {
		return c.Usage
	}
	if c.XGroq != nil {
		return c.XGroq.Usage
	}
	return nil
}
