// Package eval loads the evaluation questions and checks them against the
// papers before any run spends tokens on them.
package eval

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"relay/internal/corpus"
)

// Question is one eval item. Unanswerable items have no paper, sections
// or key phrases: the only correct reply is a refusal.
type Question struct {
	ID         string   `json:"id"`
	Split      string   `json:"split"` // "dev" tunes T and settings; "heldout" is reported
	Answerable bool     `json:"answerable"`
	Author     string   `json:"author"` // "placeholder" until replaced by hand-written ones
	Kind       string   `json:"kind"`   // fact, why, trap-*, near-miss, wrong-paper, off-topic
	Question   string   `json:"question"`
	Paper      string   `json:"paper"`       // arXiv id without version
	Sections   []string `json:"sections"`    // acceptable sections (or named parts like "Abstract")
	KeyPhrases []string `json:"key_phrases"` // each must appear in a correct answer
}

// Load reads a JSON array of questions.
func Load(path string) ([]Question, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var qs []Question
	if err := json.Unmarshal(b, &qs); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return qs, nil
}

// Validate checks every question is well formed and that its gold answer
// really is in the paper: each section exists and each key phrase occurs
// in that paper's text. It returns all problems, not just the first.
func Validate(qs []Question, docs []corpus.Doc) error {
	byPaper := map[string]corpus.Doc{}
	for _, d := range docs {
		byPaper[d.Paper] = d
	}
	var errs []string
	bad := func(q Question, format string, a ...any) {
		errs = append(errs, q.ID+": "+fmt.Sprintf(format, a...))
	}
	seen := map[string]bool{}
	for _, q := range qs {
		if q.ID == "" || seen[q.ID] {
			bad(q, "missing or duplicate id")
		}
		seen[q.ID] = true
		if q.Split != "dev" && q.Split != "heldout" {
			bad(q, "split %q, want dev or heldout", q.Split)
		}
		if strings.TrimSpace(q.Question) == "" {
			bad(q, "empty question")
		}
		if !q.Answerable {
			if q.Paper != "" || len(q.Sections) > 0 || len(q.KeyPhrases) > 0 {
				bad(q, "unanswerable question must not name a paper, sections or key phrases")
			}
			continue
		}
		doc, ok := byPaper[q.Paper]
		if !ok {
			bad(q, "unknown paper %q", q.Paper)
			continue
		}
		if len(q.Sections) == 0 || len(q.KeyPhrases) == 0 {
			bad(q, "answerable question needs sections and key phrases")
		}
		text := normalize(docText(doc))
		for _, s := range q.Sections {
			if !hasSection(doc, s) {
				bad(q, "section %q not found in %s", s, q.Paper)
			}
		}
		for _, p := range q.KeyPhrases {
			if !strings.Contains(text, normalize(p)) {
				bad(q, "key phrase %q not found in %s", p, q.Paper)
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%d problems:\n  %s", len(errs), strings.Join(errs, "\n  "))
	}
	return nil
}

// normalize lowercases and collapses whitespace, the same comparison the
// grader uses for key phrases.
func normalize(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

func docText(d corpus.Doc) string {
	var b strings.Builder
	for _, bl := range d.Blocks {
		b.WriteString(bl.Text)
		b.WriteByte(' ')
	}
	return b.String()
}

// hasSection accepts a numbered section ("3.2.1") or a named part ("Abstract").
func hasSection(d corpus.Doc, s string) bool {
	for _, bl := range d.Blocks {
		if bl.Section == s || (bl.Section == "" && bl.Title == s) {
			return true
		}
	}
	return false
}
