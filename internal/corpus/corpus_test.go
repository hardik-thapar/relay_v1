package corpus

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func words(prefix string, n int) string {
	ws := make([]string, n)
	for i := range ws {
		ws[i] = fmt.Sprintf("%s%d", prefix, i)
	}
	return strings.Join(ws, " ")
}

func TestCitation(t *testing.T) {
	tests := []struct {
		c    Chunk
		want string
	}{
		{Chunk{Paper: "1706.03762", Version: 7, Section: "3.2.1", PageStart: 4, PageEnd: 4}, "[1706.03762v7, §3.2.1, p.4]"},
		{Chunk{Paper: "2410.00037", Version: 1, Title: "Abstract", PageStart: 1, PageEnd: 2}, "[2410.00037v1, Abstract, pp.1-2]"},
	}
	for _, tt := range tests {
		if got := tt.c.Citation(); got != tt.want {
			t.Errorf("got %q, want %q", got, tt.want)
		}
	}
}

func TestWindows(t *testing.T) {
	tests := []struct {
		n, size, overlap int
		want             [][2]int
	}{
		{0, 10, 2, nil},
		{5, 10, 2, [][2]int{{0, 5}}},
		{10, 10, 2, [][2]int{{0, 10}}},
		{11, 10, 2, [][2]int{{0, 10}, {8, 11}}},
		{20, 10, 0, [][2]int{{0, 10}, {10, 20}}},
	}
	for _, tt := range tests {
		if got := windows(tt.n, tt.size, tt.overlap); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("windows(%d,%d,%d) = %v, want %v", tt.n, tt.size, tt.overlap, got, tt.want)
		}
	}
}

func TestBuildRespectsSectionsAndTracksPages(t *testing.T) {
	doc := Doc{Paper: "1234.5678", Version: 2, Blocks: []Block{
		{Page: 1, Section: "1", Title: "Intro", Text: words("a", 6)},
		{Page: 2, Section: "1", Title: "Intro", Text: words("b", 6)}, // same section, next page
		{Page: 2, Section: "2", Title: "Method", Text: words("c", 3)},
	}}
	chunks, err := Build([]Doc{doc}, 8, 2)
	if err != nil {
		t.Fatal(err)
	}
	// Section 1 has 12 words: windows [0,8) and [6,12). Section 2: [0,3).
	type got struct {
		id, sec   string
		from, to  int
		nWords    int
		firstWord string
	}
	var gs []got
	for _, c := range chunks {
		f := strings.Fields(c.Text)
		gs = append(gs, got{c.ID, c.Section, c.PageStart, c.PageEnd, len(f), f[0]})
	}
	want := []got{
		{"1234.5678v2#0", "1", 1, 2, 8, "a0"}, // a0..a5 (p1) + b0,b1 (p2)
		{"1234.5678v2#1", "1", 2, 2, 6, "b0"}, // overlap restarts at word 6 = b0
		{"1234.5678v2#2", "2", 2, 2, 3, "c0"}, // new section, new chunk
	}
	if !reflect.DeepEqual(gs, want) {
		t.Errorf("got  %+v\nwant %+v", gs, want)
	}
}

func TestBuildRejectsBadSizes(t *testing.T) {
	for _, c := range [][2]int{{0, 0}, {10, 10}, {10, -1}} {
		if _, err := Build(nil, c[0], c[1]); err == nil {
			t.Errorf("size=%d overlap=%d should fail", c[0], c[1])
		}
	}
}

// TestRealCorpus checks the committed parsed papers chunk sanely.
func TestRealCorpus(t *testing.T) {
	docs, err := LoadDir("../../data/parsed")
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 3 {
		t.Fatalf("want 3 papers, got %d", len(docs))
	}
	for _, size := range []int{150, 300} {
		chunks, err := Build(docs, size, size/5)
		if err != nil {
			t.Fatal(err)
		}
		ids := map[string]bool{}
		for _, c := range chunks {
			n := len(strings.Fields(c.Text))
			if n == 0 || n > size || ids[c.ID] || c.PageStart > c.PageEnd {
				t.Fatalf("bad chunk %s: words=%d dup=%v pages=%d-%d", c.ID, n, ids[c.ID], c.PageStart, c.PageEnd)
			}
			ids[c.ID] = true
		}
		t.Logf("size %d: %d chunks", size, len(chunks))
	}
}
