// Package results writes results.csv: one row per request, the single
// source every table is computed from (DESIGN.md §6).
package results

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
	"time"
)

// Columns: the brief's required columns in its order, then our extras.
var Columns = []string{
	"request_id", "scenario", "source", "start_time", "first_token_ms", "total_ms",
	"status", "retries", "tokens_in", "tokens_out", "retrieval_setting", "answer_grade",
	// extras
	"question_id", "queue_wait_ms", "model_ms", "queue_len_at_arrival",
	"ladder_level", "n_429_seen", "error_detail", "start_lag_ms", "top_norm",
}

// Allowed values, from the brief.
var (
	Statuses = map[string]bool{"ok": true, "rate_limited": true, "timeout": true, "error": true}
	Grades   = map[string]bool{"": true, "correct": true, "wrong": true, "refused_correctly": true, "refused_wrongly": true}
	Sources  = map[string]bool{"real": true, "stand-in": true}
)

// Row is one request. Durations are measured from the request's
// *scheduled* start, so a late generator cannot hide queueing.
// FirstTokenMS < 0 means no token ever reached the person (written empty).
type Row struct {
	RequestID        string
	Scenario         string
	Source           string // "real" or "stand-in"
	Start            time.Time
	FirstTokenMS     int64
	TotalMS          int64
	Status           string
	Retries          int
	TokensIn         int
	TokensOut        int
	RetrievalSetting string
	AnswerGrade      string // only for eval rows

	QuestionID        string
	QueueWaitMS       int64
	ModelMS           int64
	QueueLenAtArrival int
	LadderLevel       int // 0..3
	N429Seen          int
	ErrorDetail       string
	StartLagMS        int64   // how late the generator started it vs schedule
	TopNorm           float64 // best normalised retrieval score, for tuning T offline
}

// Validate rejects rows the tables could not use.
func (r Row) Validate() error {
	switch {
	case r.RequestID == "":
		return fmt.Errorf("empty request_id")
	case !Statuses[r.Status]:
		return fmt.Errorf("%s: bad status %q", r.RequestID, r.Status)
	case !Grades[r.AnswerGrade]:
		return fmt.Errorf("%s: bad answer_grade %q", r.RequestID, r.AnswerGrade)
	case !Sources[r.Source]:
		return fmt.Errorf("%s: bad source %q", r.RequestID, r.Source)
	case r.LadderLevel < 0 || r.LadderLevel > 3:
		return fmt.Errorf("%s: bad ladder_level %d", r.RequestID, r.LadderLevel)
	}
	return nil
}

func (r Row) record() []string {
	i := strconv.Itoa
	i64 := func(v int64) string { return strconv.FormatInt(v, 10) }
	firstToken := ""
	if r.FirstTokenMS >= 0 {
		firstToken = i64(r.FirstTokenMS)
	}
	return []string{
		r.RequestID, r.Scenario, r.Source, r.Start.UTC().Format(time.RFC3339Nano), firstToken, i64(r.TotalMS),
		r.Status, i(r.Retries), i(r.TokensIn), i(r.TokensOut), r.RetrievalSetting, r.AnswerGrade,
		r.QuestionID, i64(r.QueueWaitMS), i64(r.ModelMS), i(r.QueueLenAtArrival),
		i(r.LadderLevel), i(r.N429Seen), r.ErrorDetail, i64(r.StartLagMS),
		strconv.FormatFloat(r.TopNorm, 'f', 4, 64),
	}
}

// Writer appends rows to one CSV file, safe for concurrent use. Each row
// is flushed at once so a crashed run keeps what it measured.
type Writer struct {
	mu sync.Mutex
	f  *os.File
	w  *csv.Writer
}

// Open appends to path, writing the header only if the file is new or
// empty, so every run lands in the same results.csv.
func Open(path string) (*Writer, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	w := &Writer{f: f, w: csv.NewWriter(f)}
	if info.Size() == 0 {
		if err := w.writeLine(Columns); err != nil {
			f.Close()
			return nil, err
		}
	}
	return w, nil
}

// Write validates and appends one row.
func (w *Writer) Write(r Row) error {
	if err := r.Validate(); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writeLine(r.record())
}

func (w *Writer) writeLine(rec []string) error {
	if err := w.w.Write(rec); err != nil {
		return err
	}
	w.w.Flush()
	return w.w.Error()
}

func (w *Writer) Close() error { return w.f.Close() }

// ReadAll parses a results file back into string records keyed by column,
// for tests and sanity checks.
func ReadAll(r io.Reader) ([]map[string]string, error) {
	recs, err := csv.NewReader(r).ReadAll()
	if err != nil || len(recs) == 0 {
		return nil, err
	}
	head := recs[0]
	out := make([]map[string]string, 0, len(recs)-1)
	for _, rec := range recs[1:] {
		m := make(map[string]string, len(head))
		for i, col := range head {
			m[col] = rec[i]
		}
		out = append(out, m)
	}
	return out, nil
}
