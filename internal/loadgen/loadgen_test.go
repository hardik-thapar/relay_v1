package loadgen

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"relay/internal/pipeline"
	"relay/internal/results"
	"relay/internal/traffic"
)

func TestArrivalsRateAndRepeatability(t *testing.T) {
	phases := []Phase{{PerMinute: 600, For: time.Minute}, {PerMinute: 0, For: time.Minute}, {PerMinute: 60, For: time.Minute}}
	a := Arrivals(phases, 7)
	b := Arrivals(phases, 7)
	if len(a) != len(b) || a[10] != b[10] {
		t.Fatal("same seed must give the same schedule")
	}
	var first, quiet, third int
	for i, off := range a {
		if i > 0 && off < a[i-1] {
			t.Fatal("offsets must be sorted")
		}
		switch {
		case off < time.Minute:
			first++
		case off < 2*time.Minute:
			quiet++
		default:
			third++
		}
	}
	// Poisson counts: 600 ± ~3σ (σ≈24.5), 60 ± ~3σ (σ≈7.7).
	if first < 525 || first > 675 || quiet != 0 || third < 37 || third > 83 {
		t.Errorf("counts per phase: %d, %d, %d", first, quiet, third)
	}
}

// slowAsker takes `d` per answer and counts how many run at once.
type slowAsker struct {
	d             time.Duration
	inFlight, max atomic.Int32
}

func (s *slowAsker) Answer(ctx context.Context, q string, deadline time.Time) pipeline.Reply {
	n := s.inFlight.Add(1)
	for {
		m := s.max.Load()
		if n <= m || s.max.CompareAndSwap(m, n) {
			break
		}
	}
	time.Sleep(s.d)
	s.inFlight.Add(-1)
	return pipeline.Reply{Status: "ok", Released: time.Now(), Level: traffic.L1Lite, Drafts: 1}
}

func openWriter(t *testing.T) (*results.Writer, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "results.csv")
	w, err := results.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	return w, path
}

func readRows(t *testing.T, path string) []map[string]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := results.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestOpenLoopDoesNotWaitForSlowAnswers(t *testing.T) {
	// 10 jobs 10 ms apart, each taking 100 ms. A closed loop would take
	// ~1 s and never overlap; an open loop overlaps them and starts each
	// on schedule.
	var jobs []Job
	for i := 0; i < 10; i++ {
		jobs = append(jobs, Job{Offset: time.Duration(i) * 10 * time.Millisecond, QuestionID: "q" + strconv.Itoa(i), Question: "?"})
	}
	a := &slowAsker{d: 100 * time.Millisecond}
	w, path := openWriter(t)
	start := time.Now()
	sum, err := Run{Scenario: "test", Source: "stand-in", Setting: "R2", Patience: time.Second, Jobs: jobs}.Execute(context.Background(), a, w)
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 400*time.Millisecond {
		t.Errorf("run took %v: requests waited for each other", took)
	}
	if a.max.Load() < 5 {
		t.Errorf("max concurrent = %d, want overlap", a.max.Load())
	}
	if sum.Sent != 10 || sum.MaxLag > 20*time.Millisecond {
		t.Errorf("summary %+v: generator started requests late", sum)
	}

	rows := readRows(t, path)
	if len(rows) != 10 {
		t.Fatalf("%d rows", len(rows))
	}
	for _, r := range rows {
		total, _ := strconv.Atoi(r["total_ms"])
		if total < 95 || r["ladder_level"] != "1" || r["first_token_ms"] != r["total_ms"] {
			t.Errorf("row %v", r)
		}
	}
}

func TestLatencyCountsFromScheduledStart(t *testing.T) {
	// A reply released 300 ms after its scheduled start is 300 ms of
	// latency, whatever the generator did in between.
	r := Run{Scenario: "s", Source: "real", Setting: "R1"}
	sched := time.Now()
	row := r.row("id", Job{QuestionID: "q1"}, sched, 5*time.Millisecond,
		pipeline.Reply{Status: "timeout", Released: sched.Add(300 * time.Millisecond)})
	if row.TotalMS != 300 || row.FirstTokenMS != -1 || row.StartLagMS != 5 {
		t.Errorf("row %+v", row)
	}
}

func TestAnswersLogKeepsTextForReview(t *testing.T) {
	w, _ := openWriter(t)
	var log bytes.Buffer
	a := &slowAsker{d: time.Millisecond}
	run := Run{Scenario: "eval", Source: "real", Setting: "R1", Patience: time.Second, Answers: &log,
		Jobs: []Job{{QuestionID: "q1", Question: "Why?"}}, Grade: func(string, pipeline.Reply) string { return "wrong" }}
	if _, err := run.Execute(context.Background(), a, w); err != nil {
		t.Fatal(err)
	}
	var got answerLine
	if err := json.Unmarshal(log.Bytes(), &got); err != nil || got.QuestionID != "q1" || got.Question != "Why?" || got.Grade != "wrong" {
		t.Errorf("answers log %q -> %+v (%v)", log.String(), got, err)
	}
}

func TestGradeOnlyWhenAsked(t *testing.T) {
	sched := time.Now()
	rep := pipeline.Reply{Status: "ok", Released: sched}
	plain := Run{Scenario: "s", Source: "real", Setting: "R1"}.row("a", Job{QuestionID: "q1"}, sched, 0, rep)
	graded := Run{Scenario: "eval", Source: "real", Setting: "R1",
		Grade: func(string, pipeline.Reply) string { return "correct" }}.row("b", Job{QuestionID: "q1"}, sched, 0, rep)
	if plain.AnswerGrade != "" || graded.AnswerGrade != "correct" {
		t.Errorf("plain %q graded %q", plain.AnswerGrade, graded.AnswerGrade)
	}
}
