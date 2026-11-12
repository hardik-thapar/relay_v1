package pipeline

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"relay/internal/corpus"
	"relay/internal/llm"
	"relay/internal/retrieve"
	"relay/internal/traffic"
)

// Setting is one retrieval configuration (DESIGN §3, Table 3).
type Setting struct {
	Name       string
	ChunkWords int
	Overlap    int
	K          int // chunks fetched
}

// Settings are the three starting configurations; no re-ranker.
var Settings = []Setting{
	{Name: "R1", ChunkWords: 150, Overlap: 30, K: 6},
	{Name: "R2", ChunkWords: 300, Overlap: 60, K: 4},
	{Name: "R3", ChunkWords: 300, Overlap: 60, K: 2},
}

func SettingByName(name string) (Setting, error) {
	for _, s := range Settings {
		if s.Name == name {
			return s, nil
		}
	}
	return Setting{}, fmt.Errorf("unknown retrieval setting %q", name)
}

// Config holds the pipeline's knobs. Threshold and the deadlines are TBD
// in DESIGN; these defaults are starting points to be tuned on the dev
// half of the eval and the probe.
type Config struct {
	Model             string
	MaxOutput         int     // max_completion_tokens: room for hidden reasoning + answer (D7)
	Threshold         float64 // T: refuse without a model call if the top Norm is below it
	LiteK             int     // chunks used at L1
	FirstTokenTimeout time.Duration
	Retry             traffic.Policy
}

func DefaultConfig() Config {
	return Config{
		Model:             "openai/gpt-oss-20b",
		MaxOutput:         600,
		Threshold:         0.13, // D15: tuned on the dev half
		LiteK:             2,
		FirstTokenTimeout: 3 * time.Second, // probe max under burst was 1.26 s
		Retry:             traffic.DefaultPolicy(),
	}
}

// Fixed replies. None of them states anything about the papers that was
// not verified, so none can be wrong.
const (
	CouldNotVerify = "I could not verify an answer from the papers, so I won't guess."
	TimeoutText    = "Sorry, this took too long. Please try again."
	ErrorText      = "Something went wrong on our side. Please try again."
)

// Pipeline answers questions. Safe for concurrent use.
type Pipeline struct {
	cfg     Config
	setting Setting
	ix      *retrieve.Index
	client  llm.Client
	lim     *traffic.Limiter
	adm     *traffic.Admission
	now     func() time.Time
}

func New(cfg Config, s Setting, chunks []corpus.Chunk, client llm.Client, lim *traffic.Limiter, adm *traffic.Admission) *Pipeline {
	return &Pipeline{cfg: cfg, setting: s, ix: retrieve.NewIndex(chunks), client: client, lim: lim, adm: adm, now: time.Now}
}

// Reply is what the person sees plus everything results.csv needs.
type Reply struct {
	Text    string
	Status  string        // ok, rate_limited, timeout, error
	Level   traffic.Level // ladder rung
	Refused bool          // "the papers do not say" or "could not verify"
	Cited   []string      // chunk ids behind the answer
	Detail  string        // why it ended this way

	TopNorm   float64 // best retrieval score, for tuning T
	Drafts    int
	Dropped   int // uncited sentences removed before showing
	TokensIn  int
	TokensOut int
	Retries   int
	N429      int
	QueueLen  int

	QueueWait       time.Duration // time waiting for the limiter
	ModelTime       time.Duration // time inside model calls
	ModelFirstToken time.Time     // first token from the model (first draft)
	Released        time.Time     // when the reply was shown: the first token the person sees
}

// Answer runs the five steps for one question that must be answered by
// deadline (the person's patience).
func (p *Pipeline) Answer(ctx context.Context, question string, deadline time.Time) (rep Reply) {
	rep.Status = "ok"
	defer func() { rep.Released = p.now() }()
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	// 1-2. Rewrite and retrieve (the tool call: our own BM25).
	hits := p.ix.Search(Rewrite(question), p.setting.K)

	// 3. Decide: weak retrieval means the papers probably don't say.
	if len(hits) > 0 {
		rep.TopNorm = hits[0].Norm
	}
	if rep.TopNorm < p.cfg.Threshold {
		rep.Text, rep.Refused = RefusalText, true
		rep.Detail = fmt.Sprintf("top retrieval score %.3f below threshold %.3f", rep.TopNorm, p.cfg.Threshold)
		return rep
	}
	chunks := chunksOf(hits)
	lite := chunks[:min(p.cfg.LiteK, len(chunks))]

	// Admission picks the ladder rung before any token is spent.
	d := p.adm.Decide(
		estimateTokens(Messages(question, chunks))+p.cfg.MaxOutput,
		estimateTokens(Messages(question, lite))+p.cfg.MaxOutput,
		deadline)
	rep.Level, rep.QueueLen, rep.Detail = d.Level, d.QueueLen, d.Reason
	switch d.Level {
	case traffic.L3RetrievalOnly:
		rep.Status, rep.Text = "rate_limited", passages(hits)
		return rep
	case traffic.L2Busy:
		rep.Status, rep.Text = "rate_limited", busy(d.RetryIn)
		return rep
	case traffic.L1Lite:
		chunks = lite
	}

	// 4-5. Draft, verify, redraft at most once (never at L1).
	maxDrafts := 2
	if d.Level == traffic.L1Lite {
		maxDrafts = 1
	}
	msgs := Messages(question, chunks)
	for {
		text, err := p.draft(ctx, msgs, &rep)
		if err != nil {
			rep.Status, rep.Detail = statusOf(err), err.Error()
			rep.Text = map[string]string{"timeout": TimeoutText, "rate_limited": busy(0), "error": ErrorText}[rep.Status]
			return rep
		}
		rep.Drafts++
		if !isRefusal(text) {
			var dropped int
			if text, dropped = KeepCited(text); dropped > 0 {
				rep.Dropped += dropped
			}
		}
		v := Verify(text, chunks)
		switch {
		case v.OK && v.Refusal:
			rep.Text, rep.Refused, rep.Detail = RefusalText, true, "model found no answer in the chunks"
			return rep
		case v.OK:
			rep.Text, rep.Cited = renderCitations(text, chunks), v.Cited
			rep.Detail = fmt.Sprintf("verified (%d uncited sentences dropped)", rep.Dropped)
			return rep
		case rep.Drafts >= maxDrafts:
			rep.Text, rep.Refused = CouldNotVerify, true
			rep.Detail = "verify failed: " + strings.Join(v.Problems, "; ")
			return rep
		}
		msgs = Redraft(msgs, text, v.Problems)
	}
}

// draft makes one model call through the queue, limiter and retry policy.
// Every attempt, including retries, waits its turn in the queue.
func (p *Pipeline) draft(ctx context.Context, msgs []llm.Message, rep *Reply) (string, error) {
	req := llm.Request{
		Model: p.cfg.Model, Messages: msgs, MaxTokens: p.cfg.MaxOutput,
		ReasoningEffort: "low", FirstTokenTimeout: p.cfg.FirstTokenTimeout,
	}
	tokens := estimateTokens(msgs) + p.cfg.MaxOutput

	resp, st, err := traffic.Do(ctx, p.cfg.Retry, func(ctx context.Context) (*llm.Response, error) {
		queued := p.now()
		res, err := p.adm.Acquire(ctx, tokens)
		rep.QueueWait += p.now().Sub(queued)
		if err != nil {
			return nil, err
		}
		resp, err := p.client.Complete(ctx, req)
		p.settle(res, resp, err)
		p.timeModel(resp, rep)
		return resp, err
	})
	rep.Retries += st.Retries
	rep.N429 += st.RateLimited
	if err != nil {
		return "", err
	}
	rep.TokensIn += resp.TokensIn
	rep.TokensOut += resp.TokensOut
	return resp.Text, nil
}

// settle tells the limiter what happened: first our own bookkeeping
// (refund or settle the reservation), then the provider's headers, which
// describe the state after this call and so must have the last word.
// (Applying headers first once let a refund undo them: a 429 said "0
// requests left today", the refund added one back, and the next person
// got "busy" instead of the day-gone passages.) Limits are only believed
// when headers actually arrived; a 429's retry hint pauses everyone.
func (p *Pipeline) settle(res *traffic.Reservation, resp *llm.Response, err error) {
	var rl *llm.RateLimitError
	switch {
	case errors.As(err, &rl):
		res.Cancel() // refused calls did not run
		if rl.Header != nil {
			p.lim.Observe(rl.Limits)
		}
		if p.cfg.Retry.UseHint { // the naive policy ignores the hint entirely
			p.lim.PauseFor(rl.RetryAfter)
		}
	case err == nil:
		res.Finish(resp.TokensIn + resp.TokensOut)
		if resp.Header != nil {
			p.lim.Observe(resp.Limits)
		}
	default:
		res.Finish(0) // it was sent; actual tokens unknown
		if resp != nil && resp.Header != nil {
			p.lim.Observe(resp.Limits)
		}
	}
}

// timeModel adds this attempt's time inside the model call.
func (p *Pipeline) timeModel(resp *llm.Response, rep *Reply) {
	if resp == nil || resp.Sent.IsZero() {
		return
	}
	end := resp.Done
	if end.IsZero() {
		end = p.now()
	}
	rep.ModelTime += end.Sub(resp.Sent)
	if rep.ModelFirstToken.IsZero() && !resp.FirstToken.IsZero() {
		rep.ModelFirstToken = resp.FirstToken
	}
}

// statusOf maps the final error to the brief's status values.
func statusOf(err error) string {
	var (
		te *llm.TimeoutError
		rl *llm.RateLimitError
	)
	switch {
	case errors.As(err, &te), errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.As(err, &rl), errors.Is(err, traffic.ErrQueueFull):
		return "rate_limited"
	default:
		return "error"
	}
}

func chunksOf(hits []retrieve.Hit) []corpus.Chunk {
	cs := make([]corpus.Chunk, len(hits))
	for i, h := range hits {
		cs[i] = h.Chunk
	}
	return cs
}

// renderCitations swaps chunk ids for readable citations:
// [1706.03762v7#9] -> [1706.03762v7, §3.2.1, p.4].
func renderCitations(text string, chunks []corpus.Chunk) string {
	byID := make(map[string]corpus.Chunk, len(chunks))
	for _, c := range chunks {
		byID[c.ID] = c
	}
	return citeRe.ReplaceAllStringFunc(text, func(m string) string {
		if c, ok := byID[m[1:len(m)-1]]; ok {
			return c.Citation()
		}
		return m
	})
}

// busy is the L2 reply. N comes from the limiter, never a guess.
func busy(retryIn time.Duration) string {
	if retryIn <= 0 {
		return "We're busy right now. Please try again shortly."
	}
	return fmt.Sprintf("We're busy right now. Please try again in %d seconds.", int(math.Ceil(retryIn.Seconds())))
}

// passages is the L3 reply: the best matching text from the papers with
// citations, no model call. It quotes only the papers, so it cannot be
// wrong; it may simply not answer the question.
func passages(hits []retrieve.Hit) string {
	const show, words = 3, 40
	var b strings.Builder
	b.WriteString("Our daily model budget is used up, so I can't write an answer. These passages from the papers best match your question:\n")
	for i, h := range hits[:min(show, len(hits))] {
		ws := strings.Fields(h.Chunk.Text)
		excerpt := strings.Join(ws[:min(words, len(ws))], " ")
		if len(ws) > words {
			excerpt += " …"
		}
		fmt.Fprintf(&b, "\n%d. \"%s\" %s", i+1, excerpt, h.Chunk.Citation())
	}
	return b.String()
}
