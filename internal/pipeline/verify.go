// Package pipeline answers a question: rewrite, retrieve, decide, draft,
// verify (DESIGN.md §2).
package pipeline

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"

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
//  5. at least one quote is 3+ words long, so the answer carries evidence;
//  6. every sentence carries a citation, so no claim rides along unbacked.
//
// Known limit: a correct quote placed next to a wrong claim passes, and a
// cited sentence can still say more than its chunk does.
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
		longest, ok := quoteInChunk(quote, chunk.Text)
		if !ok {
			v.Problems = append(v.Problems, fmt.Sprintf("the quote %q is not word for word in [%s]", quote, id))
			continue
		}
		if longest >= minEvidenceWords {
			evidence = true
		}
	}
	if !evidence && len(cites) > 0 {
		v.Problems = append(v.Problems, fmt.Sprintf("no exact quote of at least %d words supports the answer", minEvidenceWords))
	}
	for _, s := range sentences(text) {
		if len(strings.Fields(s)) >= minClaimWords && !citeRe.MatchString(s) {
			v.Problems = append(v.Problems, fmt.Sprintf("this sentence has no citation: %q", s))
		}
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

// ellipsis splits a quote into the passages it joins ("A … B").
var ellipsis = regexp.MustCompile(`\s*(?:…|\.\.\.)\s*`)

// quoteInChunk checks a quote against its chunk. A quote that joins
// passages with an ellipsis is checked piece by piece; every piece must be
// in the chunk word for word. Case is ignored (writers lowercase a quote's
// first word to fit their sentence) and so is typography. It returns the
// word count of the longest piece, which decides whether it is evidence.
func quoteInChunk(quote, chunkText string) (int, bool) {
	source := strings.ToLower(normalize(chunkText))
	longest := 0
	for _, piece := range ellipsis.Split(trimEnd(quote), -1) {
		piece = strings.ToLower(normalize(trimEnd(piece)))
		if piece == "" {
			continue
		}
		if !strings.Contains(source, piece) {
			return 0, false
		}
		longest = max(longest, len(strings.Fields(piece)))
	}
	return longest, longest > 0
}

// KeepCited drops sentences that cite nothing and returns the rest with
// how many were dropped. The pipeline shows less rather than show a claim
// with nothing behind it (DESIGN §1: give less, never something wrong).
// What remains still goes through Verify.
func KeepCited(text string) (string, int) {
	var kept []string
	dropped := 0
	for _, s := range sentences(text) {
		if len(strings.Fields(s)) >= minClaimWords && !citeRe.MatchString(s) {
			dropped++
			continue
		}
		kept = append(kept, s)
	}
	return strings.Join(kept, " "), dropped
}

// minClaimWords: shorter fragments ("Fig." left over from splitting
// "Fig. 2") are not claims and need no citation.
const minClaimWords = 4

// sentences splits text at . ! or ? followed by whitespace, never inside
// a quotation, so a full stop inside a quoted passage does not split it.
func sentences(text string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	rs := []rune(text)
	for i, r := range rs {
		cur.WriteRune(r)
		switch r {
		case '"':
			inQuote = !inQuote
		case '“':
			inQuote = true
		case '”':
			inQuote = false
		case '.', '!', '?':
			if !inQuote && (i+1 == len(rs) || unicode.IsSpace(rs[i+1])) {
				out = append(out, strings.TrimSpace(cur.String()))
				cur.Reset()
			}
		}
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		out = append(out, s)
	}
	return out
}

// typography maps characters the PDFs and the model write differently.
// Every Unicode hyphen and dash maps to "-": the model writes U+2011
// (non-breaking hyphen) in "Mamba‑2" where the paper has "Mamba-2", which
// once refused three correct held-out answers.
var typography = strings.NewReplacer(
	"\u2019", "'", "\u2018", "'", "\u201C", `"`, "\u201D", `"`,
	"\u2010", "-", "\u2011", "-", "\u2012", "-", "\u2013", "-", "\u2014", "-", "\u2212", "-",
	"\u00A0", " ", "\u202F", " ",
)

// normalize collapses whitespace and unifies typography.
func normalize(s string) string {
	return strings.Join(strings.Fields(typography.Replace(s)), " ")
}

// trimEnd drops punctuation a writer puts inside the closing quote mark
// ("…as follows.") that may not follow the words in the source.
func trimEnd(s string) string {
	return strings.TrimRight(strings.TrimSpace(s), ".,;:!?")
}
