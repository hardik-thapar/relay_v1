package results

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func row(id string) Row {
	return Row{
		RequestID: id, Scenario: "steady", Source: "stand-in",
		Start:        time.Date(2026, 11, 12, 10, 0, 0, 5e6, time.UTC),
		FirstTokenMS: 412, TotalMS: 980, Status: "ok", TokensIn: 1500, TokensOut: 120,
		RetrievalSetting: "R2", ErrorDetail: `quote "x", missing`,
	}
}

func TestRequiredColumnsComeFirstInBriefOrder(t *testing.T) {
	brief := strings.Split("request_id,scenario,source,start_time,first_token_ms,total_ms,status,retries,tokens_in,tokens_out,retrieval_setting,answer_grade", ",")
	if !reflect.DeepEqual(Columns[:len(brief)], brief) {
		t.Errorf("columns = %v", Columns)
	}
	if len(row("x").record()) != len(Columns) {
		t.Error("record and header lengths differ")
	}
}

func TestAppendWritesHeaderOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results.csv")
	for run := 0; run < 2; run++ {
		w, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := w.Write(row(fmt.Sprintf("r%d", run))); err != nil {
			t.Fatal(err)
		}
		w.Close()
	}
	f, _ := os.Open(path)
	defer f.Close()
	rows, err := ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0]["request_id"] != "r0" || rows[1]["request_id"] != "r1" {
		t.Fatalf("rows = %v", rows)
	}
	r := rows[0]
	if r["start_time"] != "2026-11-12T10:00:00.005Z" || r["first_token_ms"] != "412" || r["error_detail"] != `quote "x", missing` {
		t.Errorf("round trip: %v", r)
	}
}

func TestNoFirstTokenIsEmpty(t *testing.T) {
	r := row("x")
	r.FirstTokenMS, r.Status = -1, "timeout"
	if got := r.record()[4]; got != "" {
		t.Errorf("first_token_ms = %q, want empty", got)
	}
}

func TestValidate(t *testing.T) {
	bad := []func(*Row){
		func(r *Row) { r.RequestID = "" },
		func(r *Row) { r.Status = "rate-limited" },
		func(r *Row) { r.AnswerGrade = "partly" },
		func(r *Row) { r.Source = "fake" },
		func(r *Row) { r.LadderLevel = 4 },
	}
	for i, mutate := range bad {
		r := row("x")
		mutate(&r)
		if r.Validate() == nil {
			t.Errorf("case %d should be rejected", i)
		}
	}
	if err := row("x").Validate(); err != nil {
		t.Error(err)
	}
}

func TestConcurrentWritesKeepRowsWhole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results.csv")
	w, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.Write(row(fmt.Sprintf("r%d", i)))
		}()
	}
	wg.Wait()
	w.Close()
	f, _ := os.Open(path)
	defer f.Close()
	rows, err := ReadAll(f)
	if err != nil || len(rows) != 50 {
		t.Fatalf("got %d rows, err %v", len(rows), err)
	}
}
