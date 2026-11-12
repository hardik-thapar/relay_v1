package pipeline

import (
	"strings"
	"testing"

	"relay/internal/corpus"
)

var chunks = []corpus.Chunk{
	{ID: "1706.03762v7#9", Text: "We suspect that for large values of dk , the dot products grow large in magnitude, pushing the softmax function into regions where it has extremely small gradients"},
	{ID: "2410.00037v1#40", Text: "Mimi’s encoder projects a 24kHz waveform to a latent representation of 12.5 frames per second"},
}

func TestVerify(t *testing.T) {
	tests := []struct {
		name    string
		answer  string
		ok      bool
		refusal bool
		problem string // substring expected in Problems when !ok
	}{
		{"good answer",
			`Large dot products push "the softmax function into regions where it has extremely small gradients" [1706.03762v7#9].`,
			true, false, ""},
		{"curly quotes, straight apostrophe, extra spaces, period inside quote",
			`The codec runs at 12.5 Hz: “Mimi's encoder projects a 24kHz   waveform.” [2410.00037v1#40]`,
			true, false, ""},
		{"refusal", "The papers do not say.", true, true, ""},
		{"refusal with explanation, different case", "the papers do NOT say. They only cover EN-DE and EN-FR.", true, true, ""},
		{"empty", "  ", false, false, "empty"},
		{"no citation", `They say "the dot products grow large in magnitude".`, false, false, "no chunk is cited"},
		{"invented chunk id", `"the dot products grow large in magnitude" [1706.03762v7#99].`, false, false, "not one of the provided chunks"},
		{"quote not in chunk", `"the dot products shrink toward zero" [1706.03762v7#9].`, false, false, "not word for word"},
		{"right quote, wrong chunk", `"the dot products grow large in magnitude" [2410.00037v1#40].`, false, false, "not word for word"},
		{"quote with no citation after it", `[1706.03762v7#9] says "the dot products grow large in magnitude".`, false, false, "no citation after it"},
		{"citation but no quote", `Scaling keeps gradients healthy [1706.03762v7#9].`, false, false, "no exact quote"},
		{"only a short quote", `It is about "the softmax" [1706.03762v7#9].`, false, false, "no exact quote"},
		{"case changed inside quote", `"The Dot Products grow large in magnitude" [1706.03762v7#9].`, false, false, "not word for word"},
		// Known limit (DESIGN §2): a true quote next to a false claim passes.
		{"known limit: correct quote, wrong claim",
			`Small dot products cause problems: "the dot products grow large in magnitude" [1706.03762v7#9].`,
			true, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := Verify(tt.answer, chunks)
			if v.OK != tt.ok || v.Refusal != tt.refusal {
				t.Fatalf("ok=%v refusal=%v problems=%q", v.OK, v.Refusal, v.Problems)
			}
			if tt.problem != "" && !strings.Contains(strings.Join(v.Problems, " | "), tt.problem) {
				t.Errorf("problems %q do not mention %q", v.Problems, tt.problem)
			}
		})
	}
}

func TestVerifyListsCitedOnce(t *testing.T) {
	a := `"the dot products grow large in magnitude" [1706.03762v7#9] and "12.5 frames per second" [2410.00037v1#40] [1706.03762v7#9].`
	v := Verify(a, chunks)
	if !v.OK || len(v.Cited) != 2 || v.Cited[0] != "1706.03762v7#9" {
		t.Errorf("verdict %+v", v)
	}
}
