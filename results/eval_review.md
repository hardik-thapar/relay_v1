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
