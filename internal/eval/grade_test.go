package eval

import "testing"

func TestGrade(t *testing.T) {
	ans := Question{Answerable: true, Paper: "2410.00037", KeyPhrases: []string{"12.5", "Frames per second"}}
	unans := Question{Answerable: false}
	moshi := []string{"2410.00037v1#40"}
	tests := []struct {
		name    string
		q       Question
		text    string
		refused bool
		cited   []string
		want    string
	}{
		{"all phrases, right paper", ans, "Mimi runs at 12.5  frames per second.", false, moshi, Correct},
		{"missing a phrase", ans, "Mimi runs at 12.5 Hz.", false, moshi, Wrong},
		{"right words, wrong paper", ans, "12.5 frames per second", false, []string{"1706.03762v7#3"}, Wrong},
		{"no citation", ans, "12.5 frames per second", false, nil, Wrong},
		{"paper id prefix is not enough", ans, "12.5 frames per second", false, []string{"2410.000371v1#2"}, Wrong},
		{"refused an answerable question", ans, "The papers do not say.", true, nil, RefusedWrongly},
		{"refused an unanswerable question", unans, "The papers do not say.", true, nil, RefusedCorrectly},
		{"answered an unanswerable question", unans, "It is O(E log V).", false, moshi, Wrong},
	}
	for _, tt := range tests {
		if got := Grade(tt.q, tt.text, tt.refused, tt.cited); got != tt.want {
			t.Errorf("%s: %s, want %s", tt.name, got, tt.want)
		}
	}
}
