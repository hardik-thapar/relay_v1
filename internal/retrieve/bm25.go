package retrieve

import (
	"math"
	"sort"

	"relay/internal/corpus"
)

// BM25 parameters, the usual defaults.
//   - k1: how fast repeated words stop adding score (saturation).
//   - b:  how much a long chunk is penalised (0 = never, 1 = fully).
const (
	k1 = 1.2
	b  = 0.75
)

// Hit is one search result.
//   - Score is raw BM25: good for ranking within one query.
//   - Norm is Score divided by the best score this query could ever get, so
//     it lies in [0,1) and can be compared across queries of different
//     lengths. The refusal threshold T is applied to Norm.
type Hit struct {
	Chunk corpus.Chunk
	Score float64
	Norm  float64
}

// Index holds the per-chunk counts BM25 needs, computed once.
// It is read-only after NewIndex, so Search is safe from many goroutines.
type Index struct {
	chunks []corpus.Chunk
	tf     []map[string]int // tf[i][term] = times term appears in chunk i
	length []int            // length[i] = tokens in chunk i
	df     map[string]int   // df[term] = number of chunks containing term
	avgLen float64
}

// NewIndex tokenizes every chunk once and records the counts.
func NewIndex(chunks []corpus.Chunk) *Index {
	ix := &Index{
		chunks: chunks,
		tf:     make([]map[string]int, len(chunks)),
		length: make([]int, len(chunks)),
		df:     map[string]int{},
	}
	total := 0
	for i, c := range chunks {
		terms := Tokenize(c.Text)
		counts := make(map[string]int, len(terms))
		for _, t := range terms {
			counts[t]++
		}
		for t := range counts {
			ix.df[t]++
		}
		ix.tf[i], ix.length[i] = counts, len(terms)
		total += len(terms)
	}
	if len(chunks) > 0 {
		ix.avgLen = float64(total) / float64(len(chunks))
	}
	return ix
}

// idf is how rare a term is. Rare terms count more. This form is never
// negative, even for a term in every chunk.
func (ix *Index) idf(term string) float64 {
	n, N := float64(ix.df[term]), float64(len(ix.chunks))
	return math.Log(1 + (N-n+0.5)/(n+0.5))
}

// Search returns up to k chunks with score > 0, best first. Equal scores
// keep corpus order so runs are reproducible.
func (ix *Index) Search(query string, k int) []Hit {
	if k <= 0 {
		return nil
	}
	terms := unique(Tokenize(query))
	if len(terms) == 0 || len(ix.chunks) == 0 {
		return nil
	}

	// The best possible score: every term matched, saturated, in a very
	// short chunk. Words the corpus never uses still count here, so an
	// off-topic query cannot reach a high Norm.
	best := 0.0
	idf := make([]float64, len(terms))
	for j, t := range terms {
		idf[j] = ix.idf(t)
		best += idf[j] * (k1 + 1)
	}

	var hits []Hit
	for i := range ix.chunks {
		s := ix.score(i, terms, idf)
		if s > 0 {
			hits = append(hits, Hit{Chunk: ix.chunks[i], Score: s, Norm: s / best})
		}
	}
	sort.SliceStable(hits, func(a, c int) bool { return hits[a].Score > hits[c].Score })
	if len(hits) > k {
		hits = hits[:k]
	}
	return hits
}

// score is the BM25 sum over query terms for chunk i:
//
//	idf(t) * tf*(k1+1) / (tf + k1*(1 - b + b*len/avgLen))
func (ix *Index) score(i int, terms []string, idf []float64) float64 {
	lengthNorm := k1 * (1 - b + b*float64(ix.length[i])/ix.avgLen)
	s := 0.0
	for j, t := range terms {
		tf := float64(ix.tf[i][t])
		if tf == 0 {
			continue
		}
		s += idf[j] * tf * (k1 + 1) / (tf + lengthNorm)
	}
	return s
}

// unique drops repeated query words: "attention attention" asks the same
// thing as "attention".
func unique(terms []string) []string {
	seen := make(map[string]bool, len(terms))
	out := terms[:0:0]
	for _, t := range terms {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}
