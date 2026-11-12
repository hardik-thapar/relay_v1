package standin

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Server implements POST /chat/completions (streaming only).
type Server struct {
	cfg Config
	now func() time.Time // replaced in tests

	mu       sync.Mutex
	rng      *rand.Rand
	inFlight int
	rpm      *bucket
	tpm      *bucket
	rpd      *bucket
	tpd      *bucket
}

func New(cfg Config) *Server {
	return newWithClock(cfg, time.Now)
}

func newWithClock(cfg Config, now func() time.Time) *Server {
	t := now()
	return &Server{
		cfg: cfg,
		now: now,
		rng: rand.New(rand.NewPCG(cfg.Seed, cfg.Seed)),
		rpm: newBucket(cfg.RPM, time.Minute, -1, t),
		tpm: newBucket(cfg.TPM, time.Minute, -1, t),
		rpd: newBucket(cfg.RPD, 24*time.Hour, cfg.StartRPD, t),
		tpd: newBucket(cfg.TPD, 24*time.Hour, cfg.StartTPD, t),
	}
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type request struct {
	Model               string    `json:"model"`
	Messages            []message `json:"messages"`
	MaxCompletionTokens int       `json:"max_completion_tokens"`
	MaxTokens           int       `json:"max_tokens"`
	Stream              bool      `json:"stream"`
}

// plan is everything decided under the lock for one admitted call.
type plan struct {
	tokensIn   int
	firstToken time.Duration
	fail500    bool
	dropAfter  int // piece index to cut the connection at; -1 = never
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/chat/completions") {
		http.NotFound(w, r)
		return
	}
	var req request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !req.Stream {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "", "body must be JSON with stream: true")
		return
	}

	p, refusal := s.admit(&req)
	if refusal != nil {
		pause(r.Context(), ms(s.cfg.RefusalMS))
		s.setLimitHeaders(w)
		w.Header().Set("retry-after", strconv.Itoa(int(math.Ceil(refusal.wait.Seconds()))))
		writeError(w, http.StatusTooManyRequests, refusal.kind, "rate_limit_exceeded", refusal.msg)
		return
	}
	defer s.release()

	if p.fail500 {
		pause(r.Context(), p.firstToken)
		writeError(w, http.StatusInternalServerError, "internal_server_error", "", "stand-in injected failure")
		return
	}
	if !pause(r.Context(), p.firstToken) {
		return // client gave up
	}
	s.stream(w, r.Context(), req, p)
}

// refusal is a 429 decision.
type refusal struct {
	kind string // "requests" or "tokens", as in Groq's body
	wait time.Duration
	msg  string
}

// admit checks every limit before taking from any, so a refused call
// consumes nothing. It also fixes this call's random draws.
func (s *Server) admit(req *request) (plan, *refusal) {
	tokensIn := s.cfg.PromptOverheadTokens
	for _, m := range req.Messages {
		tokensIn += int(float64(len(m.Content)) / s.cfg.CharsPerToken)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()

	if s.cfg.MaxInFlight > 0 && s.inFlight >= s.cfg.MaxInFlight {
		return plan{}, &refusal{"requests", time.Second, fmt.Sprintf("Too many requests in flight: Limit %d.", s.cfg.MaxInFlight)}
	}
	checks := []struct {
		b    *bucket
		n    float64
		kind string
		name string
	}{
		{s.rpm, 1, "requests", "requests per minute (RPM)"},
		{s.rpd, 1, "requests", "requests per day (RPD)"},
		{s.tpm, float64(tokensIn), "tokens", "tokens per minute (TPM)"},
		{s.tpd, float64(tokensIn), "tokens", "tokens per day (TPD)"},
	}
	for _, c := range checks {
		if wait := c.b.wait(c.n, now); wait > 0 {
			msg := fmt.Sprintf("Rate limit reached for model `%s` on %s: Limit %d, Requested %d. Please try again in %s.",
				req.Model, c.name, int(c.b.capacity), int(c.n), wait.Round(time.Millisecond))
			return plan{}, &refusal{c.kind, wait, msg}
		}
	}
	for _, c := range checks {
		c.b.charge(c.n, now)
	}

	s.inFlight++
	others := s.inFlight - 1
	base := s.cfg.FirstTokenMS[s.rng.IntN(len(s.cfg.FirstTokenMS))]
	p := plan{
		tokensIn:   tokensIn,
		firstToken: ms(base + s.cfg.InputMSPerToken*float64(tokensIn) + s.cfg.LoadMSPerInFlight*float64(others)),
		fail500:    s.rng.Float64() < s.cfg.ServerErrorRate,
		dropAfter:  -1,
	}
	if s.rng.Float64() < s.cfg.DropRate {
		p.dropAfter = s.rng.IntN(4) // cut within the first few pieces
	}
	return p, nil
}

func (s *Server) release() {
	s.mu.Lock()
	s.inFlight--
	s.mu.Unlock()
}

// stream writes the answer as SSE pieces of a few words each, then usage.
func (s *Server) stream(w http.ResponseWriter, ctx context.Context, req request, p plan) {
	maxOut := req.MaxCompletionTokens
	if maxOut == 0 {
		maxOut = req.MaxTokens
	}
	text, tokensOut := s.budget(answer(req.Messages), maxOut)

	s.setLimitHeaders(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher := w.(http.Flusher)

	for i, piece := range pieces(text, 3) {
		if i == p.dropAfter {
			panic(http.ErrAbortHandler) // net/http closes the connection
		}
		if i > 0 && !pause(ctx, ms(s.cfg.OutputMSPerToken*s.tokens(piece))) {
			return
		}
		writeChunk(w, map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": piece}}}})
		flusher.Flush()
	}

	s.mu.Lock()
	s.tpm.charge(float64(tokensOut), s.now())
	s.tpd.charge(float64(tokensOut), s.now())
	s.mu.Unlock()

	writeChunk(w, map[string]any{"choices": []any{}, "usage": map[string]int{"prompt_tokens": p.tokensIn, "completion_tokens": tokensOut}})
	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

// budget applies max output tokens. Hidden reasoning is spent first, so a
// small budget gives an empty answer, as the probe showed (DECISIONS D7).
func (s *Server) budget(text string, maxOut int) (string, int) {
	content := int(s.tokens(text))
	total := s.cfg.ReasoningTokens + content
	if maxOut <= 0 || total <= maxOut {
		return text, total
	}
	room := maxOut - s.cfg.ReasoningTokens
	if room <= 0 {
		return "", maxOut
	}
	runes := []rune(text) // cut on a character boundary, never inside one
	cut := int(float64(room) * s.cfg.CharsPerToken)
	return string(runes[:min(cut, len(runes))]), maxOut
}

func (s *Server) tokens(text string) float64 {
	return math.Ceil(float64(len(text)) / s.cfg.CharsPerToken)
}

// setLimitHeaders mirrors Groq: *-requests = per day, *-tokens = per minute.
func (s *Server) setLimitHeaders(w http.ResponseWriter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	h := w.Header()
	if s.rpd != nil {
		h.Set("x-ratelimit-limit-requests", strconv.Itoa(int(s.rpd.capacity)))
		h.Set("x-ratelimit-remaining-requests", strconv.Itoa(s.rpd.remaining(now)))
		h.Set("x-ratelimit-reset-requests", s.rpd.untilFull(now).Round(10*time.Millisecond).String())
	}
	if s.tpm != nil {
		h.Set("x-ratelimit-limit-tokens", strconv.Itoa(int(s.tpm.capacity)))
		h.Set("x-ratelimit-remaining-tokens", strconv.Itoa(s.tpm.remaining(now)))
		h.Set("x-ratelimit-reset-tokens", s.tpm.untilFull(now).Round(time.Millisecond).String())
	}
}

// pieces splits text into groups of n words, keeping the separating
// spaces so the concatenation equals the original.
func pieces(text string, n int) []string {
	words := strings.SplitAfter(text, " ")
	var out []string
	for i := 0; i < len(words); i += n {
		out = append(out, strings.Join(words[i:min(i+n, len(words))], ""))
	}
	return out
}

func writeChunk(w http.ResponseWriter, v any) {
	b, _ := json.Marshal(v)
	fmt.Fprintf(w, "data: %s\n\n", b)
}

func writeError(w http.ResponseWriter, status int, typ, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body := map[string]any{"error": map[string]string{"message": msg, "type": typ, "code": code}}
	json.NewEncoder(w).Encode(body)
}

// pause sleeps for d unless ctx ends first; it reports whether it slept fully.
func pause(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func ms(v float64) time.Duration { return time.Duration(v * float64(time.Millisecond)) }
