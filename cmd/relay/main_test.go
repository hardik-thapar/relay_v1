package main

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"

	"relay/internal/standin"
)

func TestRunAgainstStandIn(t *testing.T) {
	cfg := standin.Default()
	cfg.FirstTokenMS = []float64{1}
	cfg.OutputMSPerToken, cfg.LoadMSPerInFlight = 0, 0
	srv := httptest.NewServer(standin.New(cfg))
	defer srv.Close()

	var out bytes.Buffer
	err := run([]string{"-base", srv.URL, "-data", "../../data/parsed", "-q", "Why does the Transformer scale dot products before the softmax?"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if !strings.Contains(s, "status=ok") || !strings.Contains(s, "[1706.03762v7, §") {
		t.Errorf("output:\n%s", s)
	}
}

func TestRunNeedsQuestion(t *testing.T) {
	if err := run([]string{"-base", "http://x"}, &bytes.Buffer{}); err == nil {
		t.Error("want error without -q")
	}
}
