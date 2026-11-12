"""Turn the three PDFs into page- and section-tagged text blocks.

Usage: python3 scripts/parse.py data/pdf data/parsed

Writes one JSON file per paper:
  {"paper": "1706.03762", "version": 7,
   "blocks": [{"page": 3, "section": "3.2.1", "title": "Scaled Dot-Product Attention",
               "text": "..."}]}

A block is a run of text sharing one page and one section. Go does the
chunking (DESIGN.md §3); this script only finds pages and headings.
"""

import json
import re
import subprocess
import sys
from pathlib import Path

PAPERS = {"1706.03762": 7, "2410.00037": 1, "2507.07955": 2}

# "3.2.1" or "A.2", optionally followed by a dot.
NUM = r"([0-9]+(?:\.[0-9]+)*|[A-H](?:\.[0-9]+)*)\.?"
NUM_ONLY = re.compile(rf"^{NUM}$")
NUM_TITLE = re.compile(rf"^{NUM}\s+(\S.*)$")
NAMED = {"abstract", "references", "acknowledgements", "acknowledgments", "appendix"}
ARXIV_STAMP = re.compile(r"^arXiv:\d{4}\.\d{5}v\d+ \[")
APPENDIX = re.compile(r"^Appendix ([A-H])\.?\s+(\S.*)$")  # Moshi style


def pdf_pages(pdf: Path) -> list[str]:
    """Run pdftotext; form feeds separate pages. Drop the empty tail."""
    out = subprocess.run(["pdftotext", str(pdf), "-"], capture_output=True, text=True, check=True).stdout
    pages = out.split("\f")
    if pages and not pages[-1].strip():
        pages.pop()
    return pages


def num_key(num: str) -> tuple[int, ...]:
    """Sortable key: digits as ints, appendix letters after all numbers."""
    return tuple(int(p) if p.isdigit() else 100 + ord(p) - ord("A") for p in num.split("."))


def is_successor(prev: tuple[int, ...], cand: tuple[int, ...], appendix_ok: bool) -> bool:
    """True if cand can follow prev in a paper's outline.

    Allowed: first child (3 -> 3.1), next sibling at any depth with at most
    one skipped number (3.2.2 -> 3.2.3, 3.3, 4 or 5), and appendix "A" after
    References. This rejects page numbers and table cells that look like
    section numbers (after 3.2 a bare "2" is not a heading).
    """
    if cand[0] >= 100 and (not prev or prev[0] < 100):
        return appendix_ok and cand == (100,)
    if not prev:
        return cand == (1,)
    d = len(cand) - 1  # depth at which cand differs
    if cand[:d] != prev[:d]:
        return False
    if len(prev) == d:
        return cand[d] == 1
    if len(prev) > d:
        return prev[d] < cand[d] <= prev[d] + 2
    return False


def looks_like_title(s: str) -> bool:
    """Short, capitalised, no sentence punctuation, mostly letters."""
    s = s.strip()
    if not (2 <= len(s) <= 80) or not s[0].isupper():
        return False
    if s[-1] in ".,;:?" or s.startswith(("Figure", "Table")) or len(s.split()) > 10:
        return False
    letters = sum(c.isalpha() or c.isspace() for c in s)
    return letters / len(s) >= 0.7


def find_heading(lines: list[str], i: int, prev, appendix_ok):
    """If a heading starts at lines[i], return (number, title, lines_used).

    Two layouts: "3.2 Title" on one line, or "3.2" alone with the title on
    the next non-empty line (within two lines).
    """
    line = lines[i].strip()
    m = APPENDIX.match(line)
    if m:
        return m.group(1), m.group(2).strip(), 1
    m = NUM_TITLE.match(line)
    if m and looks_like_title(m.group(2)) and is_successor(prev, num_key(m.group(1)), appendix_ok):
        return m.group(1), m.group(2).strip(), 1
    m = NUM_ONLY.match(line)
    if m and is_successor(prev, num_key(m.group(1)), appendix_ok):
        for j in range(i + 1, min(i + 3, len(lines))):
            nxt = lines[j].strip()
            if nxt:
                if looks_like_title(nxt):
                    return m.group(1), nxt, j - i + 1
                break
    return None


def keep_line(s: str) -> bool:
    """Drop figure debris (1-2 char fragments) and the arXiv margin stamp."""
    return len(s.replace(" ", "")) >= 3 and not ARXIV_STAMP.match(s)


def running_headers(pages: list[str]) -> set[str]:
    """Lines repeated on more than a third of pages (and at least 3):
    page headers and footers that would make every chunk match the title."""
    counts: dict[str, int] = {}
    for page in pages:
        for line in {l.strip() for l in page.split("\n") if l.strip()}:
            counts[line] = counts.get(line, 0) + 1
    limit = max(3, len(pages) // 3)
    return {line for line, n in counts.items() if n > limit}


def clean(lines: list[str]) -> str:
    """Join lines, mend end-of-line hyphenation, collapse whitespace."""
    text = "\n".join(lines)
    text = re.sub(r"(\w)-\n(\w)", r"\1\2", text)
    return re.sub(r"\s+", " ", text).strip()


def parse_pages(pages: list[str]) -> list[dict]:
    """Walk pages line by line, cutting a new block at each page or heading."""
    blocks: list[dict] = []
    section, title, prev, appendix_ok = "", "Front matter", (), False
    repeated = running_headers(pages)

    def flush(page: int, buf: list[str]):
        text = clean(buf)
        if text:
            blocks.append({"page": page, "section": section, "title": title, "text": text})
        buf.clear()

    for page_no, page in enumerate(pages, start=1):
        lines = page.split("\n")
        buf: list[str] = []
        i = 0
        while i < len(lines):
            stripped = lines[i].strip()
            h = find_heading(lines, i, prev, appendix_ok)
            if h:
                flush(page_no, buf)
                section, title, used = h
                prev = num_key(section)
                appendix_ok = appendix_ok or prev[0] >= 100
                i += used
                continue
            if stripped.lower() in NAMED:
                flush(page_no, buf)
                section, title = "", stripped.title()
                appendix_ok = appendix_ok or stripped.lower() in ("references", "appendix")
                i += 1
                continue
            if keep_line(stripped) and stripped not in repeated:
                buf.append(stripped)
            i += 1
        flush(page_no, buf)
    return blocks


def main(pdf_dir: str, out_dir: str) -> None:
    out = Path(out_dir)
    out.mkdir(parents=True, exist_ok=True)
    for paper, version in PAPERS.items():
        blocks = parse_pages(pdf_pages(Path(pdf_dir) / f"{paper}v{version}.pdf"))
        doc = {"paper": paper, "version": version, "blocks": blocks}
        path = out / f"{paper}v{version}.json"
        path.write_text(json.dumps(doc, indent=1, ensure_ascii=False))
        heads = [(b["section"], b["title"]) for b in blocks]
        print(f"{path}: {len(blocks)} blocks, {len(dict.fromkeys(heads))} sections")


if __name__ == "__main__":
    main(*sys.argv[1:3])
