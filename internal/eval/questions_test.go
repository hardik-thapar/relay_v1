package eval

import (
	"strings"
	"testing"

	"relay/internal/corpus"
)

var toyDocs = []corpus.Doc{{Paper: "1111.11111", Version: 1, Blocks: []corpus.Block{
	{Page: 1, Title: "Abstract", Text: "We   present a Model."},
	{Page: 2, Section: "2.1", Title: "Method", Text: "It uses cosine similarity between adjacent vectors."},
}}}

func answerable() Question {
	return Question{ID: "q1", Split: "dev", Answerable: true, Question: "How?",
		Paper: "1111.11111", Sections: []string{"2.1", "Abstract"}, KeyPhrases: []string{"Cosine  Similarity", "present a model"}}
}

func TestValidateAcceptsGoodQuestions(t *testing.T) {
	refusal := Question{ID: "q2", Split: "heldout", Question: "Off topic?"}
	if err := Validate([]Question{answerable(), refusal}, toyDocs); err != nil {
		t.Error(err)
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	cases := map[string]func(*Question){
		"split":       func(q *Question) { q.Split = "test" },
		"unknown":     func(q *Question) { q.Paper = "9999.99999" },
		"section":     func(q *Question) { q.Sections = []string{"7.7"} },
		"key phrase":  func(q *Question) { q.KeyPhrases = []string{"dot product"} },
		"needs":       func(q *Question) { q.KeyPhrases = nil },
		"empty":       func(q *Question) { q.Question = " " },
		"must not":    func(q *Question) { q.Answerable = false },
		"missing or ": func(q *Question) { q.ID = "" },
	}
	for want, mutate := range cases {
		q := answerable()
		mutate(&q)
		err := Validate([]Question{q}, toyDocs)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v", want, err)
		}
	}
	dup := []Question{answerable(), answerable()}
	if err := Validate(dup, toyDocs); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("duplicate ids: got %v", err)
	}
}

// TestCommittedQuestions holds eval/questions.json to DESIGN §7:
// 20 questions, 14 answerable and 6 not, split evenly with the same mix.
func TestCommittedQuestions(t *testing.T) {
	qs, err := Load("../../eval/questions.json")
	if err != nil {
		t.Fatal(err)
	}
	docs, err := corpus.LoadDir("../../data/parsed")
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(qs, docs); err != nil {
		t.Fatal(err)
	}
	count := map[string]map[bool]int{"dev": {}, "heldout": {}}
	for _, q := range qs {
		count[q.Split][q.Answerable]++
	}
	for _, split := range []string{"dev", "heldout"} {
		if count[split][true] != 7 || count[split][false] != 3 {
			t.Errorf("%s: %d answerable, %d unanswerable; want 7 and 3", split, count[split][true], count[split][false])
		}
	}
}
