package eval

import "strings"

// Grades, as the brief names them.
const (
	Correct          = "correct"
	Wrong            = "wrong"
	RefusedCorrectly = "refused_correctly"
	RefusedWrongly   = "refused_wrongly"
)

// Grade scores one reply against its question (DESIGN §7). An answer is
// correct only if every key phrase is in it AND it cites a chunk from the
// expected paper: right words with the wrong source do not count.
// Any non-refusal to an unanswerable question is wrong.
//
// It is a first pass: rows where a human disagrees are re-read by hand.
func Grade(q Question, text string, refused bool, cited []string) string {
	if refused {
		if q.Answerable {
			return RefusedWrongly
		}
		return RefusedCorrectly
	}
	if !q.Answerable {
		return Wrong
	}
	answer := normalize(text)
	for _, p := range q.KeyPhrases {
		if !strings.Contains(answer, normalize(p)) {
			return Wrong
		}
	}
	for _, id := range cited {
		if strings.HasPrefix(id, q.Paper+"v") {
			return Correct
		}
	}
	return Wrong
}
