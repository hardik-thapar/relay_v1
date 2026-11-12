package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"relay/internal/llm"
)

// fakeProvider answers OK, except calls numbered in [limitFrom, limitTo)
// which get a Groq-shaped 429.
func fakeProvider(limitFrom, limitTo int64) http.HandlerFunc {
	var n atomic.Int64
	return func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		i := n.Add(1)
		w.Header().Set("x-ratelimit-remaining-requests", "999")
		w.Header().Set("Set-Cookie", "secret=1")
		if i >= limitFrom && i < limitTo {
			w.Header().Set("retry-after", "3")
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"error":{"message":"Rate limit reached","type":"tokens"}}`)
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"blue\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}
}

func TestRunRecordsEveryCallAndStopsRecoveryOnSuccess(t *testing.T) {
	// Calls 1-2 seq, 3 big, 4-7 rpm burst, then recovery from 8.
	// Calls 6..9 are limited: the burst sees 2 limited, recovery sees
	// 8,9 limited then 10 ok.
	srv := httptest.NewServer(fakeProvider(6, 10))
	defer srv.Close()

	cfg := config{Model: "m", SeqN: 2, BigN: 1, BigWords: 10, BurstN: 4, TPMN: 2, RecoverMax: 10}
	var out bytes.Buffer
	p := &prober{cfg: cfg, c: llm.NewHTTP(srv.URL, ""), out: &out}
	p.run(context.Background())

	count := map[string]map[string]int{}
	for _, r := range p.recs {
		if count[r.Phase] == nil {
			count[r.Phase] = map[string]int{}
		}
		count[r.Phase][r.Status]++
		if r.Status == "rate_limited" && (r.RetryAfterMS != 3000 || !strings.Contains(r.Body, "Rate limit")) {
			t.Errorf("429 record missing retry-after or body: %+v", r)
		}
		if _, ok := r.Headers["set-cookie"]; ok {
			t.Error("cookies must not be recorded")
		}
	}
	want := map[string]map[string]int{
		"seq":         {"ok": 2},
		"big":         {"ok": 1},
		"rpm":         {"ok": 2, "rate_limited": 2},
		"recover-rpm": {"rate_limited": 2, "ok": 1},
		"tpm":         {"ok": 2},
		"recover-tpm": {"ok": 1},
	}
	for ph, w := range want {
		for st, n := range w {
			if count[ph][st] != n {
				t.Errorf("%s/%s = %d, want %d (all: %v)", ph, st, count[ph][st], n, count)
			}
		}
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != len(p.recs) {
		t.Fatalf("jsonl has %d lines, want %d", len(lines), len(p.recs))
	}
	var r record
	if err := json.Unmarshal([]byte(lines[0]), &r); err != nil || r.TokensIn != 10 || r.TokensOut != 2 {
		t.Errorf("first line: %+v, err %v", r, err)
	}
}

func TestPct(t *testing.T) {
	xs := []int64{50, 10, 40, 20, 30}
	tests := map[int]int64{50: 30, 95: 50, 100: 50, 1: 10}
	for p, want := range tests {
		if got := pct(xs, p); got != want {
			t.Errorf("pct(%d) = %d, want %d", p, got, want)
		}
	}
	if pct(nil, 50) != 0 {
		t.Error("empty slice should give 0")
	}
}

func TestFillerLength(t *testing.T) {
	if n := len(strings.Fields(filler(123))); n != 123 {
		t.Errorf("got %d words", n)
	}
}
