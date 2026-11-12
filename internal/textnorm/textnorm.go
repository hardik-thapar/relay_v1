// Package textnorm is the one definition of "the same text" used by both
// verify (is this quote in the chunk?) and grading (is this key phrase in
// the answer?), so the two can never disagree about a hyphen again.
package textnorm

import "strings"

// typography maps characters the PDFs and the model write differently.
// Every Unicode hyphen and dash maps to "-": the model writes U+2011
// (non-breaking hyphen) in "Mamba‑2" where the paper has "Mamba-2".
var typography = strings.NewReplacer(
	"’", "'", "‘", "'", "“", `"`, "”", `"`,
	"‐", "-", "‑", "-", "‒", "-", "–", "-", "—", "-", "−", "-",
	" ", " ", " ", " ",
)

// Normalize unifies typography and collapses whitespace. Case is kept.
func Normalize(s string) string {
	return strings.Join(strings.Fields(typography.Replace(s)), " ")
}

// Fold is Normalize plus lowercasing, for case-blind comparisons.
func Fold(s string) string { return strings.ToLower(Normalize(s)) }
