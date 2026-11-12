"""Choose the refusal threshold T from a T=0 eval run on the dev half.

Usage: python3 scripts/tune_t.py results/results.csv eval/questions.json

With T=0 every question reached the model, and each row recorded its
top_norm. A threshold T would have refused, without a model call, every
question whose top_norm < T. This replays that for a grid of T values:
no extra calls are needed to compare thresholds.
"""

import csv
import json
import sys
from collections import defaultdict


def replay(rows, t):
    """Grades as they would be under threshold t."""
    out = []
    for r in rows:
        if float(r["top_norm"]) < t:
            out.append("refused_wrongly" if r["_answerable"] else "refused_correctly")
        else:
            out.append(r["answer_grade"])
    return out


def main(results_path, questions_path, split="dev"):
    questions = {q["id"]: q for q in json.load(open(questions_path))}
    rows = [r for r in csv.DictReader(open(results_path, newline=""))
            if r["scenario"] == "eval" and r["question_id"] in questions
            and questions[r["question_id"]]["split"] == split]
    for r in rows:
        r["_answerable"] = questions[r["question_id"]]["answerable"]

    grid = [round(0.05 * i, 2) for i in range(0, 11)]
    for setting, rs in sorted(_group(rows).items()):
        print(f"\n{setting} ({len(rs)} {split} questions)")
        print("   T    correct  wrong  refused_ok  refused_wrongly")
        for t in grid:
            g = defaultdict(int)
            for grade in replay(rs, t):
                g[grade] += 1
            print(f"  {t:.2f}  {g['correct']:7d}  {g['wrong']:5d}  {g['refused_correctly']:10d}  {g['refused_wrongly']:15d}")
        print("  top_norm per question:", ", ".join(f"{r['question_id']}={float(r['top_norm']):.3f}{'' if r['_answerable'] else '(U)'}"
                                                    for r in sorted(rs, key=lambda r: float(r['top_norm']))))


def _group(rows):
    g = defaultdict(list)
    for r in rows:
        g[r["retrieval_setting"]].append(r)
    return g


if __name__ == "__main__":
    main(*sys.argv[1:4])
