package retrieve

import (
	"math"
	"sync"
	"testing"

	"relay/internal/corpus"
)

// Toy corpus. Expected scores were computed independently in Python from
// the formula (k1=1.2, b=0.75, idf = ln(1 + (N-n+0.5)/(n+0.5))):
//
//	query        c0        c1        c2        norm(c2)
//	attention    1.302837  0         0
//	model        0.127035  0.127035  0.148744
//	speech model 0.127035  0.127035  1.241313  0.506329
//	speech dijkstra                  1.092569  0.162281
var toy = []corpus.Chunk{
	{ID: "c0", Text: "Attention, attention: the model."}, // -> attention attention model
	{ID: "c1", Text: "Model training data"},
	{ID: "c2", Text: "speech model"},
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-5 }

func ids(hits []Hit) []string {
	var out []string
	for _, h := range hits {
		out = append(out, h.Chunk.ID)
	}
	return out
}

func TestScoresMatchHandComputation(t *testing.T) {
	ix := NewIndex(toy)
	hits := ix.Search("attention", 10)
	if len(hits) != 1 || hits[0].Chunk.ID != "c0" || !near(hits[0].Score, 1.302837) {
		t.Fatalf("attention: %+v", hits)
	}
	hits = ix.Search("speech model", 10)
	if !near(hits[0].Score, 1.241313) || !near(hits[0].Norm, 0.506329) || !near(hits[1].Score, 0.127035) {
		t.Errorf("speech model: %+v", hits)
	}
}

func TestShorterChunkWinsAndTiesKeepCorpusOrder(t *testing.T) {
	// "model" appears once in every chunk: c2 is shortest so ranks first;
	// c0 and c1 tie and must keep corpus order for reproducible runs.
	got := ids(NewIndex(toy).Search("model", 10))
	want := []string{"c2", "c0", "c1"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestUnknownTermsLowerNormNotScore(t *testing.T) {
	ix := NewIndex(toy)
	known := ix.Search("speech model", 1)[0]
	unknown := ix.Search("speech dijkstra", 1)[0]
	if !near(unknown.Score, 1.092569) || !near(unknown.Norm, 0.162281) {
		t.Errorf("speech dijkstra: %+v", unknown)
	}
	if unknown.Norm >= known.Norm {
		t.Error("a query word the papers never use should lower the normalised score")
	}
}

func TestKAndEmptyQueries(t *testing.T) {
	ix := NewIndex(toy)
	if got := ix.Search("model", 2); len(got) != 2 {
		t.Errorf("k=2 gave %d hits", len(got))
	}
	for _, q := range []string{"", "the of and", "dijkstra"} {
		if got := ix.Search(q, 5); len(got) != 0 {
			t.Errorf("%q should match nothing, got %v", q, ids(got))
		}
	}
	if got := ix.Search("model", 0); got != nil {
		t.Error("k=0 should return nil")
	}
}

func TestRepeatedQueryWordsCountOnce(t *testing.T) {
	ix := NewIndex(toy)
	a, b := ix.Search("attention", 1)[0], ix.Search("attention attention attention", 1)[0]
	if !near(a.Score, b.Score) {
		t.Errorf("repeating a word changed the score: %v vs %v", a.Score, b.Score)
	}
}

func TestEmptyIndex(t *testing.T) {
	if got := NewIndex(nil).Search("model", 3); len(got) != 0 {
		t.Errorf("empty index gave %v", got)
	}
}

func TestConcurrentSearch(t *testing.T) {
	ix := NewIndex(toy)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ix.Search("speech model attention", 3)
		}()
	}
	wg.Wait()
}
