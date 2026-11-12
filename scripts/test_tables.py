import unittest

from tables import answered, pct, table_2b, table_3, triple

def row(**kw):
    base = {"request_id": "r", "scenario": "s", "source": "stand-in", "start_time": "2026-11-12T10:00:00Z",
            "first_token_ms": "100", "total_ms": "100", "status": "ok", "retries": "0", "tokens_in": "1000",
            "tokens_out": "100", "retrieval_setting": "R2", "answer_grade": "", "queue_len_at_arrival": "0",
            "queue_wait_ms": "0", "model_ms": "90"}
    base.update(kw)
    return base

class Percentile(unittest.TestCase):
    def test_nearest_rank(self):
        xs = [15, 20, 35, 40, 50]
        self.assertEqual(pct(xs, 50), 35)
        self.assertEqual(pct(xs, 95), 50)
        self.assertEqual(pct(xs, 1), 15)
        self.assertIsNone(pct([], 50))

    def test_triple_not_measured(self):
        self.assertEqual(triple([]), "not measured")

class Tables(unittest.TestCase):
    def test_2b_counts_and_tokens_only_over_answers(self):
        rows = [row(), row(status="rate_limited", tokens_in="0", tokens_out="0", queue_len_at_arrival="7"),
                row(status="timeout", retries="2", tokens_in="0", tokens_out="0"), row(tokens_out="0", tokens_in="0")]
        line = table_2b(rows)[2]
        # Statuses ok, rate_limited, timeout, ok: 50% ok. Tokens average
        # only the one row that produced an answer (1000 + 100).
        self.assertIn("| s (stand-in) | 50.0 | 25.0 | 25.0 | 0.50 | 7 | 1100 |", line)

    def test_cost_at_list_price(self):
        from tables import cost_per_1000
        # 1000 in, 100 out per answer: 1000 answers = 1M in ($0.075) + 0.1M out ($0.03).
        self.assertEqual(cost_per_1000([row()]), "$0 free; $0.105 at list")

    def test_answered_excludes_refusals(self):
        self.assertTrue(answered(row()))
        self.assertFalse(answered(row(tokens_out="0")))
        self.assertFalse(answered(row(status="rate_limited")))

    def test_3_grades(self):
        rows = [row(answer_grade="correct"), row(answer_grade="wrong"),
                row(answer_grade="refused_correctly", tokens_out="0"), row(answer_grade="refused_wrongly", tokens_out="0")]
        self.assertIn("| R2 | 4 | 25.0 | 25.0 | 25.0 | 1100 |", table_3(rows)[2])

class Tune(unittest.TestCase):
    def test_replay_refuses_below_t(self):
        from tune_t import replay
        rows = [{"top_norm": "0.10", "answer_grade": "wrong", "_answerable": False},
                {"top_norm": "0.10", "answer_grade": "correct", "_answerable": True},
                {"top_norm": "0.40", "answer_grade": "correct", "_answerable": True}]
        self.assertEqual(replay(rows, 0.0), ["wrong", "correct", "correct"])
        self.assertEqual(replay(rows, 0.2), ["refused_correctly", "refused_wrongly", "correct"])

if __name__ == "__main__":
    unittest.main()
