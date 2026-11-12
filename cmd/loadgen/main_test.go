package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"relay/internal/results"
	"relay/internal/standin"
	"relay/internal/traffic"
)

// fastWorld swaps the named scenario for one with no delays and huge
// limits: the calibrated world really queues for minutes at 8K TPM,
// which proves the limiter but is no use in a unit test.
func fastWorld(t *testing.T, name string) {
	t.Helper()
	orig := scenarios[name]
	fast := orig
	fast.limits = traffic.Limits{RPM: 1e6, TPM: 1e9}
	fast.provider = func(c *standin.Config) {
		orig.provider(c)
		c.RPM, c.TPM, c.RPD, c.TPD = 1e6, 1e9, 1e6, 1e9
		c.FirstTokenMS = []float64{1}
		c.OutputMSPerToken, c.LoadMSPerInFlight, c.RefusalMS = 0, 0, 0
	}
	scenarios[name] = fast
	t.Cleanup(func() { scenarios[name] = orig })
}

func runArgs(t *testing.T, extra ...string) ([]map[string]string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "results.csv")
	args := append([]string{"-out", path, "-answers", filepath.Join(t.TempDir(), "answers.jsonl"), "-questions", "../../eval/questions.json", "-data", "../../data/parsed"}, extra...)
	var out bytes.Buffer
	if err := run(args, &out); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := results.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return rows, out.String()
}

func TestEvalOnStandInGradesEveryQuestion(t *testing.T) {
	fastWorld(t, "eval")
	rows, out := runArgs(t, "-scenario", "eval", "-split", "dev")
	if len(rows) != 10 {
		t.Fatalf("%d rows, want 10 dev questions", len(rows))
	}
	for _, r := range rows {
		if r["answer_grade"] == "" || r["scenario"] != "eval" || r["source"] != "stand-in" || r["retrieval_setting"] != "R1" {
			t.Errorf("row %v", r)
		}
	}
	if !strings.Contains(out, "eval: 10 requests") {
		t.Errorf("report: %s", out)
	}
}

func TestSmokeScenarioOpenLoop(t *testing.T) {
	fastWorld(t, "smoke")
	rows, out := runArgs(t, "-scenario", "smoke", "-policy", "naive")
	if len(rows) < 3 {
		t.Fatalf("only %d rows", len(rows))
	}
	for _, r := range rows {
		if r["scenario"] != "smoke-naive" || r["answer_grade"] != "" {
			t.Errorf("row %v", r)
		}
	}
	if !strings.Contains(out, "smoke-naive:") {
		t.Errorf("report: %s", out)
	}
}

func TestRealOnlyForCheapScenarios(t *testing.T) {
	err := run([]string{"-scenario", "overload", "-source", "real"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "daily budget") {
		t.Errorf("overload on real Groq must be refused, got %v", err)
	}
}

func TestScenariosAreComplete(t *testing.T) {
	for _, name := range []string{"steady", "burst", "overload", "halved", "daily-exhausted"} {
		sc, ok := scenarios[name]
		if !ok || sc.arrivals == nil || sc.provider == nil || sc.patience == 0 {
			t.Errorf("scenario %q incomplete", name)
			continue
		}
		if n := len(sc.arrivals(1)); n < 5 {
			t.Errorf("%s: only %d arrivals", name, n)
		}
	}
}
