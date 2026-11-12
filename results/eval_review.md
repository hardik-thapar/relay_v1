# Manual review of graded eval rows

DESIGN §7: a script grades (all key phrases present + a citation from the expected paper); I read every row where it might be wrong. **Grades in `results.csv` are left as the script gave them** so the tables stay recomputable; disagreements are recorded here.

**These rows use the 20 PLACEHOLDER questions** (`author: placeholder` in `eval/questions.json`), not the final hand-written set.

## Dev run, T = 0, 2026-11-12

The dev run (`eval-R1/R2/R3-*`) predates the answers log, so its answer texts were not saved. The three R2 rows below were re-asked with `cmd/relay -setting R2 -t 0` (temperature 0) and read; the answers may differ slightly from the graded run.

| Question | Script grade (R2) | My reading | Why |
|---|---|---|---|
| q02 big Transformer parameters (213) | refused_wrongly | **agree** | The number is only in Table 3, flattened by pdftotext into number soup. The model refused instead of guessing: a safe failure. Table-only facts are a retrieval/parsing limit. |
| q04 Moshi Inner Monologue | wrong | **disagree: correct** | Answer names the Inner Monologue and the text stream generated as the first subsequence, both cited correctly. It says "text stream" where the key phrase says "text tokens": the key phrase was too narrow. |
| q05 H-Net chunk boundaries | wrong | **partly correct** | Says a boundary is set when similarity drops and the probability passes 0.5, cited to §2.2.1, but never names cosine similarity between adjacent vectors. Incomplete, not false. |

**Lesson for the final questions:** key phrases must be words any correct answer must use, not the paper's exact term for a concept a correct answer could name differently.

Also seen: the q05 re-ask hit a real Groq 429. Each `cmd/relay` run starts a fresh limiter that knows nothing of the previous processes' tokens that minute; Groq refused, and the retry honoured the 2 s hint and succeeded.

## Held-out run (R1, T = 0.13, after the hyphen fix in verify), 2026-11-12

Answer texts are in `results/answers.jsonl` (`eval-heldout-*`). Script grades: 4 correct, 4 wrong, 2 correctly refused. Every `wrong` row read:

| Question | Script grade | My reading | Why |
|---|---|---|---|
| q15 ratio loss | wrong | **partly correct** | Quotes that the loss "guides the model toward the desired compression ratio", cited §2.2.3. Does not name the problem (over/under-compression); key phrase "ratio loss" absent. |
| q16 encoder/decoder layers | wrong | **disagree: correct** | Quotes "We employ Mamba‑2 layers … as the primary building blocks for the encoder and decoder networks", §2.1.2. The model wrote U+2011 in "Mamba‑2"; **the grader had the same hyphen bug verify had**. Fixed (shared `internal/textnorm`); the row's grade in `results.csv` is left as the old grader wrote it. |
| q17 main network must be a Transformer? | wrong | **disagree: correct** | Quotes "can employ any sequence mixing architecture" and "allows straightforward substitution with alternative architectures", §2.1.2. Key phrase "any standard architecture" was too narrow. |
| q19 H-Net latency in spoken dialogue (unanswerable) | wrong | **agree: wrong, and the most instructive row** | Answered "theoretical latency of 160ms", citing the **Moshi** abstract. "Real-time spoken dialogue" is Moshi's vocabulary, so BM25 pulled Moshi chunks; the quote is real, so verify passed it. This is DESIGN §2's known limit (true quote, false claim) and a naive-retriever failure: the question names one paper and the answer comes from another. |

**My reading of held-out:** 6 correct (q11–q14, q16, q17), 1 partly correct (q15), 1 wrong (q19), 2 correctly refused (q18, q20). The script says 4 / 4 / 2. With placeholder questions and 10 items, both are rough.

**Fix this suggests (not built):** check that a cited chunk's paper matches a paper the question names (q19 names H-Net, cites Moshi). Cheap string check; would have refused q19.
