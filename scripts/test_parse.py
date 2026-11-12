import unittest

from parse import (clean, find_heading, is_successor, looks_like_title, num_key,
                   parse_pages, running_headers)


class Successor(unittest.TestCase):
    def ok(self, prev, cand, appendix_ok=False):
        return is_successor(num_key(prev) if prev else (), num_key(cand), appendix_ok)

    def test_valid_steps(self):
        for prev, cand in [("", "1"), ("3", "3.1"), ("3.1", "3.2"), ("3.2.2", "3.2.3"),
                           ("3.2.2", "3.3"), ("3.2.2", "4"), ("3", "5")]:
            self.assertTrue(self.ok(prev, cand), f"{prev} -> {cand}")

    def test_rejects_page_numbers_and_jumps(self):
        for prev, cand in [("", "2"), ("3.2", "2"), ("3", "3.2"), ("3", "6"), ("3", "3.1.1"),
                           ("3.1", "4.1"), ("6", "2023")]:
            self.assertFalse(self.ok(prev, cand), f"{prev} -> {cand}")

    def test_appendix_only_after_references(self):
        self.assertFalse(self.ok("7", "A"))
        self.assertTrue(self.ok("7", "A", appendix_ok=True))
        self.assertFalse(self.ok("7", "B", appendix_ok=True))
        self.assertTrue(self.ok("A", "A.1"))
        self.assertTrue(self.ok("A.4", "B"))


class Title(unittest.TestCase):
    def test_titles(self):
        for s in ["Introduction", "Scaled Dot-Product Attention", "Dynamic Chunking (DC)"]:
            self.assertTrue(looks_like_title(s), s)

    def test_not_titles(self):
        for s in ["output values. These are", "Figure 1: The Transformer", "How does it behave?",
                  "To illustrate why the dot products get large, assume that the components are random",
                  "23.75 24.6 25.16", "x"]:
            self.assertFalse(looks_like_title(s), s)


class Heading(unittest.TestCase):
    def test_one_line_layout(self):
        self.assertEqual(find_heading(["3.2.1 Architecture"], 0, (3, 2), False), ("3.2.1", "Architecture", 1))

    def test_two_line_layout_skips_blank(self):
        lines = ["3.2", "", "Attention", "body"]
        self.assertEqual(find_heading(lines, 0, (3, 1), False), ("3.2", "Attention", 3))

    def test_page_number_followed_by_prose_is_not_heading(self):
        self.assertIsNone(find_heading(["4", "", "output values. These are"], 0, (3, 2, 2), False))

    def test_moshi_appendix_layout(self):
        self.assertEqual(find_heading(["Appendix B. Audio matching"], 0, (100,), True), ("B", "Audio matching", 1))


class Cleaning(unittest.TestCase):
    def test_hyphen_and_space(self):
        self.assertEqual(clean(["the atten-", "tion  layer", "works"]), "the attention layer works")

    def test_running_headers(self):
        pages = ["Paper Title\nalpha", "Paper Title\nbeta", "Paper Title\ngamma", "Paper Title\ndelta"]
        self.assertEqual(running_headers(pages), {"Paper Title"})


class Pages(unittest.TestCase):
    def test_blocks_split_on_page_and_heading(self):
        pages = [
            "Abstract\nWe study things here.\n1\n\nIntroduction\nIntro text on page one.",
            "More intro text.\n2 Method\nMethod text.",
        ]
        blocks = parse_pages(pages)
        got = [(b["page"], b["section"], b["title"], b["text"]) for b in blocks]
        self.assertEqual(got, [
            (1, "", "Abstract", "We study things here."),
            (1, "1", "Introduction", "Intro text on page one."),
            (2, "1", "Introduction", "More intro text."),
            (2, "2", "Method", "Method text."),
        ])


if __name__ == "__main__":
    unittest.main()
