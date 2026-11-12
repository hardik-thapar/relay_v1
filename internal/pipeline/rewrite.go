package pipeline

import (
	"strings"

	"relay/internal/retrieve"
)

// filler are words people put around a question that say nothing about
// what is being asked. The tokenizer's stopwords are general English;
// these are specific to asking about papers.
var filler = map[string]bool{
	"please": true, "can": true, "could": true, "would": true, "you": true, "tell": true,
	"me": true, "i": true, "explain": true, "describe": true, "give": true, "want": true,
	"know": true, "about": true, "paper": true, "papers": true, "authors": true,
	"according": true, "say": true, "says": true, "said": true, "detail": true, "details": true,
}

// Rewrite turns a question into a search query by rules, no model
// (DESIGN §2 step 1): lowercase, split like the index does, drop
// stopwords and question filler, keep everything else, including numbers
// and technical terms. If nothing would be left, it keeps the tokens.
func Rewrite(question string) string {
	tokens := retrieve.Tokenize(question)
	var kept []string
	for _, t := range tokens {
		if !filler[t] {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		kept = tokens
	}
	return strings.Join(kept, " ")
}
