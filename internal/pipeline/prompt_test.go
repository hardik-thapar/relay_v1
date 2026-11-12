package pipeline

import (
	"regexp"
	"strings"
	"testing"

	"relay/internal/corpus"
	"relay/internal/llm"
)

func TestRewrite(t *testing.T) {
	tests := map[string]string{
		"Can you please explain why the Transformer scales dot products?":  "transformer scales dot products",
		"According to the paper, what frame rate does Mimi use (12.5 Hz?)": "frame rate mimi use 12 5 hz",
		"What is the ratio loss?": "ratio loss",
		"Does H-Net NOT use BPE?": "h net not use bpe",
		// Only filler left: keep the tokens rather than search for nothing.
		"Tell me about it": "tell me about",
	}
	for in, want := range tests {
		if got := Rewrite(in); got != want {
			t.Errorf("Rewrite(%q) = %q, want %q", in, got, want)
		}
	}
}

// The stand-in recognises chunk lines with this pattern; the prompt must
// produce lines it can read.
var standinChunkLine = regexp.MustCompile(`^\[([^\]\s]+#\d+)\]\s+(.+)$`)

func TestMessagesRenderOneChunkPerLine(t *testing.T) {
	cs := []corpus.Chunk{{ID: "1706.03762v7#3", Text: "alpha beta"}, {ID: "2410.00037v1#40", Text: "gamma"}}
	msgs := Messages("Why?", cs)
	if len(msgs) != 2 || msgs[0].Role != "system" || !strings.Contains(msgs[0].Content, RefusalText) {
		t.Fatalf("messages: %+v", msgs)
	}
	var found []string
	for _, line := range strings.Split(msgs[1].Content, "\n") {
		if m := standinChunkLine.FindStringSubmatch(line); m != nil {
			found = append(found, m[1])
		}
	}
	if len(found) != 2 || found[0] != cs[0].ID || found[1] != cs[1].ID {
		t.Errorf("chunk lines found: %v", found)
	}
}

func TestRedraftKeepsHistoryAndNamesProblems(t *testing.T) {
	msgs := Messages("Why?", nil)
	out := Redraft(msgs, "bad draft", []string{"quote not found"})
	if len(out) != 4 || out[2].Content != "bad draft" || !strings.Contains(out[3].Content, "quote not found") {
		t.Errorf("redraft: %+v", out)
	}
	if len(msgs) != 2 {
		t.Error("Redraft must not modify the original messages")
	}
}

func TestEstimateTokensMatchesProbeCalibration(t *testing.T) {
	// The probe measured 74 prompt tokens for the single message "Say ok.".
	got := estimateTokens([]llm.Message{{Role: "user", Content: "Say ok."}})
	if got < 72 || got > 76 {
		t.Errorf("estimate %d, probe measured 74", got)
	}
}
