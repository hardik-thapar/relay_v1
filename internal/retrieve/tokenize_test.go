package retrieve

import (
	"reflect"
	"testing"
)

func TestTokenize(t *testing.T) {
	tests := map[string][]string{
		"":                         {},
		"The Transformer":          {"transformer"},
		"Multi-Head Attention":     {"multi", "head", "attention"},
		"scaled by 1/sqrt(d_k)":    {"scaled", "1", "sqrt", "d", "k"},
		"BLEU of 28.4 on WMT 2014": {"bleu", "28", "4", "wmt", "2014"},
		"does NOT use recurrence":  {"not", "use", "recurrence"},
		"Mimi’s RVQ, Moshi's “Inner Monologue”": {"mimi", "s", "rvq", "moshi", "s", "inner", "monologue"},
		"Cernocký": {"cernocký"},
	}
	for in, want := range tests {
		got := Tokenize(in)
		if len(got) == 0 && len(want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Tokenize(%q) = %q, want %q", in, got, want)
		}
	}
}
