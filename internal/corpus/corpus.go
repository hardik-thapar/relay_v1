// Package corpus loads the parsed papers and cuts them into citable chunks.
package corpus

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Block is one run of text on one page under one heading, as written by
// scripts/parse.py.
type Block struct {
	Page    int    `json:"page"`
	Section string `json:"section"` // "3.2.1", "A.1", or "" for named parts
	Title   string `json:"title"`   // "Scaled Dot-Product Attention", "Abstract"
	Text    string `json:"text"`
}

// Doc is one parsed paper.
type Doc struct {
	Paper   string  `json:"paper"`
	Version int     `json:"version"`
	Blocks  []Block `json:"blocks"`
}

// Chunk is the unit of retrieval and citation.
type Chunk struct {
	ID        string // "1706.03762v7#12", unique across the corpus
	Paper     string
	Version   int
	Section   string
	Title     string
	PageStart int
	PageEnd   int
	Text      string
}

// Citation renders the reference shown to the person, e.g.
// "[1706.03762v7, §3.2.1, p.4]" or "[2410.00037v1, Abstract, pp.1-2]".
func (c Chunk) Citation() string {
	where := c.Title
	if c.Section != "" {
		where = "§" + c.Section
	}
	pages := fmt.Sprintf("p.%d", c.PageStart)
	if c.PageEnd != c.PageStart {
		pages = fmt.Sprintf("pp.%d-%d", c.PageStart, c.PageEnd)
	}
	return fmt.Sprintf("[%sv%d, %s, %s]", c.Paper, c.Version, where, pages)
}

// LoadDir reads every *.json doc in dir, sorted by file name so chunk IDs
// are stable between runs.
func LoadDir(dir string) ([]Doc, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no parsed papers in %s", dir)
	}
	sort.Strings(paths)
	docs := make([]Doc, 0, len(paths))
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		var d Doc
		if err := json.Unmarshal(b, &d); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		docs = append(docs, d)
	}
	return docs, nil
}

// Build cuts every doc into chunks of at most size words, consecutive
// chunks sharing overlap words. A chunk never crosses a section boundary,
// so each chunk has exactly one section to cite; it may span pages.
func Build(docs []Doc, size, overlap int) ([]Chunk, error) {
	if size <= 0 || overlap < 0 || overlap >= size {
		return nil, fmt.Errorf("need size > overlap >= 0, got size=%d overlap=%d", size, overlap)
	}
	var out []Chunk
	for _, d := range docs {
		n := 0 // per-paper chunk counter for IDs
		for _, run := range sectionRuns(d.Blocks) {
			for _, w := range windows(len(run.words), size, overlap) {
				out = append(out, Chunk{
					ID:        fmt.Sprintf("%sv%d#%d", d.Paper, d.Version, n),
					Paper:     d.Paper,
					Version:   d.Version,
					Section:   run.section,
					Title:     run.title,
					PageStart: run.pages[w[0]],
					PageEnd:   run.pages[w[1]-1],
					Text:      strings.Join(run.words[w[0]:w[1]], " "),
				})
				n++
			}
		}
	}
	return out, nil
}

// run is the words of consecutive blocks under one heading, with the page
// each word came from.
type run struct {
	section, title string
	words          []string
	pages          []int
}

// sectionRuns merges consecutive blocks with the same heading.
func sectionRuns(blocks []Block) []run {
	var runs []run
	for _, b := range blocks {
		if len(runs) == 0 || runs[len(runs)-1].section != b.Section || runs[len(runs)-1].title != b.Title {
			runs = append(runs, run{section: b.Section, title: b.Title})
		}
		r := &runs[len(runs)-1]
		for _, w := range strings.Fields(b.Text) {
			r.words = append(r.words, w)
			r.pages = append(r.pages, b.Page)
		}
	}
	return runs
}

// windows returns [start, end) word ranges of at most size, stepping by
// size-overlap, with the last window ending exactly at n.
func windows(n, size, overlap int) [][2]int {
	var out [][2]int
	for start := 0; start < n; start += size - overlap {
		end := min(start+size, n)
		out = append(out, [2]int{start, end})
		if end == n {
			break
		}
	}
	return out
}
