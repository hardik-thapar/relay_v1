// Package pipeline answers a question: rewrite, retrieve, decide, draft,
// verify (DESIGN.md §2).
package pipeline

import (
	"fmt"
	"regexp"
	"strings"

	"relay/internal/corpus"
)

// RefusalText is the exact sentence the model is told to use when the
// chunks do not answer the question.
const RefusalText = "The papers do not say."

// minEvidenceWords: a quote shorter than this proves too little ("the
// model") to count as the evidence every answer must carry.
const minEvidenceWords = 3

// Verdict is the outcome of checking one draft.
type Verdict struct {
	OK       bool
	Refusal  bool     // the draft is a refusal (OK is then true)
	Cited    []string // chunk ids cited, first-seen order, no repeats
	Problems []string // why it failed, in words the redraft prompt can use
}

var (
	citeRe  = regexp.MustCompile(`\[([^\[\]\s]+#\d+)\]`) // [1706.03762v7#12]
	quoteRe = regexp.MustCompile(`"([^"]+)"|“([^”]+)”`)  // "..." or “...”
)

// Verify checks a draft with plain string rules, no model:
//  1. it is not empty;
//  2. a refusal is accepted as is;
//  3. it cites at least one chunk, and only chunks that were retrieved;
//  4. every quote appears word for word in the chunk cited right after it;
//  5. at least one quote is 3+ words long, so the answer carries evidence.
//
// Known limit: a correct quote placed next to a wrong claim passes.
func Verify(answer string, retrieved []corpus.Chunk) Verdict {
	var v Verdict
	text := strings.TrimSpace(answer)
	if text == "" {
		return fail(v, "the answer is empty")
	}
	if isRefusal(text) {
		return Verdict{OK: true, Refusal: true}
	}

	byID := make(map[string]corpus.Chunk, len(retrieved))
	for _, c := range retrieved {
		byID[c.ID] = c
	}

	cites := citeRe.FindAllStringSubmatchIndex(text, -1)
	if len(cites) == 0 {
		v.Problems = append(v.Problems, "no chunk is cited; cite each claim as [chunk id]")
	}
	seen := map[string]bool{}
	for _, m := range cites {
		id := text[m[2]:m[3]]
		if seen[id] {
			continue
		}
		seen[id] = true
		v.Cited = append(v.Cited, id)
		if _, ok := byID[id]; !ok {
			v.Problems = append(v.Problems, fmt.Sprintf("[%s] is cited but was not one of the provided chunks", id))
		}
	}

	evidence := false
	for _, q := range quoteRe.FindAllStringSubmatchIndex(text, -1) {
		quote := group(text, q)
		id, ok := citationAfter(text, cites, q[1])
		if !ok {
			v.Problems = append(v.Problems, fmt.Sprintf("the quote %q has no citation after it", quote))
			continue
		}
		chunk, known := byID[id]
		if !known {
			continue // already reported above
		}
		if !strings.Contains(normalize(chunk.Text), normalize(trimEnd(quote))) {
			v.Problems = append(v.Problems, fmt.Sprintf("the quote %q is not word for word in [%s]", quote, id))
			continue
		}
		if len(strings.Fields(quote)) >= minEvidenceWords {
			evidence = true
		}
	}
	if !evidence && len(cites) > 0 {
		v.Problems = append(v.Problems, fmt.Sprintf("no exact quote of at least %d words supports the answer", minEvidenceWords))
	}

	v.OK = len(v.Problems) == 0
	return v
}

func fail(v Verdict, problem string) Verdict {
	v.Problems = append(v.Problems, problem)
	return v
}

// isRefusal accepts the refusal sentence at the start, ignoring case and
// spacing, so "The papers do not say. They discuss X instead." still counts.
// The person is shown RefusalText only: anything after it is unverified.
func isRefusal(text string) bool {
	return strings.HasPrefix(strings.ToLower(normalize(text)), strings.ToLower(normalize(RefusalText)))
}

// group returns whichever quote alternative matched (straight or curly).
func group(text string, m []int) string {
	if m[2] >= 0 {
		return text[m[2]:m[3]]
	}
	return text[m[4]:m[5]]
}

// citationAfter returns the id of the first citation starting at or after
// pos: the citation a quote belongs to.
func citationAfter(text string, cites [][]int, pos int) (string, bool) {
	for _, c := range cites {
		if c[0] >= pos {
			return text[c[2]:c[3]], true
		}
	}
	return "", false
}

// typography maps characters the PDFs and the model write differently.
var typography = strings.NewReplacer(
	"’", "'", "‘", "'", "“", `"`, "”", `"`,
	"–", "-", "—", "-", "‐", "-", " ", " ",
)

// normalize collapses whitespace and unifies typography. Case is kept:
// a quote must be word for word.
func normalize(s string) string {
	return strings.Join(strings.Fields(typography.Replace(s)), " ")
}

// trimEnd drops punctuation a writer puts inside the closing quote mark
// ("…as follows.") that may not follow the words in the source.
func trimEnd(s string) string {
	return strings.TrimRight(strings.TrimSpace(s), ".,;:!?")
}
