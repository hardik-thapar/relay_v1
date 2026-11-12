// Package retrieve finds the chunks most relevant to a query with BM25.
package retrieve

import (
	"strings"
	"unicode"
)

// stopwords are dropped from queries and chunks alike. Kept small on
// purpose: words like "not", "without" or "only" change meaning and stay.
var stopwords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true, "at": true, "be": true,
	"by": true, "do": true, "does": true, "for": true, "from": true, "how": true, "in": true,
	"is": true, "it": true, "its": true, "of": true, "on": true, "or": true, "that": true,
	"the": true, "their": true, "this": true, "to": true, "was": true, "were": true,
	"what": true, "when": true, "which": true, "who": true, "why": true, "with": true,
}

// Tokenize lowercases text and splits it on anything that is not a letter
// or digit, dropping stopwords. "Multi-Head" -> ["multi", "head"],
// "d_k" -> ["d", "k"], "28.4" -> ["28", "4"]. No stemming: "chunk" and
// "chunking" are different terms (a known limit, see DESIGN §3).
func Tokenize(text string) []string {
	fields := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := fields[:0]
	for _, f := range fields {
		if !stopwords[f] {
			out = append(out, f)
		}
	}
	return out
}
