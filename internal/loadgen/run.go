package loadgen

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"relay/internal/pipeline"
	"relay/internal/results"
)

// Asker is the pipeline as the generator sees it.
type Asker interface {
	Answer(ctx context.Context, question string, deadline time.Time) pipeline.Reply
}

// Job is one scheduled request.
type Job struct {
	Offset     time.Duration // from the run's start
	QuestionID string
	Question   string
}

// Run describes one scenario run.
type Run struct {
	Scenario string
	Source   string // "real" or "stand-in"
	Setting  string // retrieval setting name
	Patience time.Duration
	Jobs     []Job

	// Grade, if set, fills answer_grade (eval rows only).
	Grade func(questionID string, r pipeline.Reply) string
}

// Summary is what the run reports at the end.
type Summary struct {
	Sent   int
	MaxLag time.Duration // worst start lateness: the lag check
}

// Execute starts every job at its scheduled time, whether or not earlier
// ones have finished (open loop), and writes one row per job. Latency is
// measured from the scheduled start, so time a request spends waiting
// because we are slow is counted against us, not hidden.
func (r Run) Execute(ctx context.Context, a Asker, w *results.Writer) (Summary, error) {
	jobs := append([]Job(nil), r.Jobs...)
	sort.SliceStable(jobs, func(i, j int) bool { return jobs[i].Offset < jobs[j].Offset })

	start := time.Now().Add(20 * time.Millisecond) // a moment to set up
	runID := start.UTC().Format("150405")
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		maxLag time.Duration
		werr   error
	)
	for i, job := range jobs {
		scheduled := start.Add(job.Offset)
		if d := time.Until(scheduled); d > 0 {
			select {
			case <-time.After(d):
			case <-ctx.Done():
				wg.Wait()
				return Summary{Sent: i, MaxLag: maxLag}, ctx.Err()
			}
		}
		lag := time.Since(scheduled)
		mu.Lock()
		maxLag = max(maxLag, lag)
		mu.Unlock()

		id := fmt.Sprintf("%s-%s-%s-%04d", r.Scenario, r.Setting, runID, i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			rep := a.Answer(ctx, job.Question, scheduled.Add(r.Patience))
			row := r.row(id, job, scheduled, lag, rep)
			if err := w.Write(row); err != nil {
				mu.Lock()
				werr = err
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return Summary{Sent: len(jobs), MaxLag: maxLag}, werr
}

// row maps a reply to the results.csv columns. The person's first token
// is when the verified reply was released (answers are buffered); a
// timeout or error shows them no answer, so first_token_ms is empty.
func (r Run) row(id string, job Job, scheduled time.Time, lag time.Duration, rep pipeline.Reply) results.Row {
	total := rep.Released.Sub(scheduled).Milliseconds()
	first := int64(-1)
	if rep.Status == "ok" || rep.Status == "rate_limited" {
		first = total
	}
	grade := ""
	if r.Grade != nil {
		grade = r.Grade(job.QuestionID, rep)
	}
	return results.Row{
		RequestID: id, Scenario: r.Scenario, Source: r.Source, Start: scheduled,
		FirstTokenMS: first, TotalMS: total, Status: rep.Status, Retries: rep.Retries,
		TokensIn: rep.TokensIn, TokensOut: rep.TokensOut, RetrievalSetting: r.Setting, AnswerGrade: grade,
		QuestionID: job.QuestionID, QueueWaitMS: rep.QueueWait.Milliseconds(), ModelMS: rep.ModelTime.Milliseconds(),
		QueueLenAtArrival: rep.QueueLen, LadderLevel: int(rep.Level), N429Seen: rep.N429,
		ErrorDetail: rep.Detail, StartLagMS: lag.Milliseconds(), TopNorm: rep.TopNorm,
	}
}
