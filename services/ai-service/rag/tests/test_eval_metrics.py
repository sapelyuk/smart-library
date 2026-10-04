#!/usr/bin/env python3
"""test_eval_metrics.py - offline proof that the RAG eval metrics mean what they claim.

Run:
    python tests/test_eval_metrics.py -v
    python -m unittest discover -s tests -v
    python -m pytest tests/test_eval_metrics.py -q

No network, no database, no API key: everything here works on the checked-in
fixture `evals/fixtures/self-retrieval-run.json`, which is a synthetic run whose
expected answers are a permutation of the real catalogue. That is deliberate - it
exists to test the SCORING, not the library, so it is a legitimate red/green
fixture rather than a claim about retrieval quality.
"""

from __future__ import annotations

import contextlib
import io
import json
import os
import sys
import tempfile
import unittest

PROJECT_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(PROJECT_ROOT, "scripts"))

import eval_metrics as em  # noqa: E402

SUITE = os.path.join(PROJECT_ROOT, "evals", "rag-eval-suite.json")
FIXTURES = os.path.join(PROJECT_ROOT, "evals", "fixtures")
PERFECT_FIXTURE = os.path.join(FIXTURES, "perfect-run.json")
DEGRADED_FIXTURE = os.path.join(FIXTURES, "degraded-run.json")
AGENT_FIXTURE = os.path.join(FIXTURES, "agent-mode-run.json")
CATALOGUE = os.path.join(PROJECT_ROOT, "samples", "books.csv")


def fixture(name):
    with open(os.path.join(FIXTURES, name), encoding="utf-8") as fh:
        return json.load(fh)


class TestScoreDefinition(unittest.TestCase):
    """precision@k and recall@k, by hand, on cases small enough to count."""

    def test_full_hit(self):
        m = em.score(["a", "b", "c"], ["a", "b"])
        self.assertEqual(m["precision"], 0.67)   # 2 relevant of 3 returned
        self.assertEqual(m["recall"], 1.0)       # 2 of 2 expected found
        self.assertEqual(m["hit"], 1)
        self.assertEqual(m["rank_first_hit"], 1)
        self.assertEqual(m["mrr"], 1.0)

    def test_hit_at_rank_three(self):
        m = em.score(["x", "y", "a"], ["a"])
        self.assertEqual(m["precision"], 0.33)
        self.assertEqual(m["recall"], 1.0)
        self.assertEqual(m["rank_first_hit"], 3)
        self.assertEqual(m["mrr"], 0.33)

    def test_nothing_retrieved(self):
        m = em.score([], ["a"])
        self.assertEqual(m["precision"], 0.0)
        self.assertEqual(m["recall"], 0.0)
        self.assertEqual(m["hit"], 0)
        self.assertEqual(m["mrr"], 0.0)

    def test_retrieved_but_all_wrong(self):
        m = em.score(["x", "y"], ["a"])
        self.assertEqual(m["precision"], 0.0)
        self.assertEqual(m["recall"], 0.0)
        self.assertEqual(m["hit"], 0)

    def test_partial_recall(self):
        m = em.score(["a", "x", "y"], ["a", "b", "c"])
        self.assertEqual(m["precision"], 0.33)
        self.assertEqual(m["recall"], 0.33)
        self.assertEqual(m["missing"], ["b", "c"])

    def test_duplicates_are_removed_before_scoring(self):
        """A recommender that repeats one book must not look precise."""
        m = em.score(["a", "a", "a"], ["a", "b", "c"])
        self.assertEqual(m["returned"], 1)
        self.assertEqual(m["precision"], 1.0)
        self.assertEqual(m["recall"], 0.33)

    def test_refuse_question_naming_nothing_passes(self):
        m = em.score([], [])
        self.assertEqual(m["precision"], 1.0)
        self.assertIsNone(m["recall"])
        self.assertIsNone(m["hit"])

    def test_refuse_question_naming_something_fails(self):
        m = em.score(["a"], [])
        self.assertEqual(m["precision"], 0.0)
        self.assertEqual(m["true_positives"], 0)

    def test_f1_is_harmonic_mean(self):
        m = em.score(["a", "x"], ["a", "b"])
        self.assertEqual(m["precision"], 0.5)
        self.assertEqual(m["recall"], 0.5)
        self.assertEqual(m["f1"], 0.5)


class TestAggregate(unittest.TestCase):
    def make(self, precision, recall, passed=True, category="taste"):
        metrics = em.score([], [])
        metrics["precision"] = precision
        metrics["recall"] = recall
        return {"category": category, "metrics": metrics, "passed": passed}

    def test_macro_average_counts_every_question_once(self):
        agg = em.aggregate([self.make(1.0, 1.0), self.make(0.0, 0.0)])
        self.assertEqual(agg["precision"], 0.5)
        self.assertEqual(agg["recall"], 0.5)
        self.assertEqual(agg["mrr"], 0.0)

    def test_empty_is_zero_not_a_crash(self):
        agg = em.aggregate([])
        self.assertEqual(agg["questions"], 0)
        self.assertEqual(agg["precision"], 0.0)

    def test_by_category_splits_correctly(self):
        by_cat = em.aggregate_by_category(
            [self.make(1.0, 1.0, category="self"), self.make(0.0, 0.0, category="taste")]
        )
        self.assertEqual(by_cat["self"]["precision"], 1.0)
        self.assertEqual(by_cat["taste"]["precision"], 0.0)
        self.assertNotIn("genre", by_cat)


class TestTitleMatching(unittest.TestCase):
    """Agent mode scores prose, so the matcher must be precise about titles."""

    def setUp(self):
        self.catalogue = em.load_catalogue(CATALOGUE)
        self.assertGreater(len(self.catalogue), 0, "samples/books.csv is missing")

    def test_normalisation(self):
        self.assertEqual(em.normalise_title("Thinking, Fast and Slow"), "thinking fast and slow")
        self.assertEqual(em.normalise_title("The City and the City"), "city and the city")
        self.assertEqual(em.normalise_title("Sapiens: A Brief History of Humankind"), "sapiens a brief history of humankind")

    def test_exact_title(self):
        found = em.mentioned_books("I recommend Piranesi by Susanna Clarke.", self.catalogue)
        self.assertEqual(found, ["piranesi"])

    def test_case_insensitive(self):
        found = em.mentioned_books("try NEUROMANCER next", self.catalogue)
        self.assertEqual(found, ["neuromancer-william-gibson"])

    def test_subtitle_truncation_still_matches(self):
        found = em.mentioned_books("Sapiens is the obvious pick.", self.catalogue)
        self.assertEqual(found, ["sapiens"])

    def test_partial_word_does_not_match(self):
        self.assertEqual(em.mentioned_books("Sapien history is odd.", self.catalogue), [])

    def test_title_inside_a_longer_title_matches_the_full_one(self):
        """A full title matches; the matcher does not prefer the inner fragment."""
        found = em.mentioned_books("The City and the City was strange.", self.catalogue)
        self.assertEqual(found, ["the-city-and-the-city"])

    def test_truncated_prefix_is_loose_by_design(self):
        """A partial title is credited loosely - see the docstring. Pinned, not hidden."""
        self.assertEqual(em.mentioned_books("The quiet city slept.", self.catalogue), ["the-city-and-the-city"])

    def test_title_that_is_simply_absent_does_not_match(self):
        self.assertEqual(em.mentioned_books("The quiet village slept.", self.catalogue), [])

    def test_order_is_first_mention(self):
        found = em.mentioned_books("First Piranesi, then Dune.", self.catalogue)
        self.assertEqual(found, ["piranesi", "dune-frank-herbert"])

    def test_distinct_books(self):
        found = em.mentioned_books("Dune and Dune again.", self.catalogue)
        self.assertEqual(found, ["dune-frank-herbert"])

    def test_question_echo_is_excluded(self):
        """An echo of the question is not a recommendation."""
        answer = "You mentioned Dune, so try Neuromancer."
        found = em.extract_mentioned_ids("I loved Dune.", answer, self.catalogue, [])
        self.assertEqual(found, ["neuromancer-william-gibson"])

    def test_explicit_also_mentions_are_excluded(self):
        answer = "Dune and Neuromancer are both here."
        found = em.extract_mentioned_ids("anything good?", answer, self.catalogue, ["dune-frank-herbert"])
        self.assertEqual(found, ["neuromancer-william-gibson"])


class TestSuiteItself(unittest.TestCase):
    """The shipped suite must be internally coherent, with no network at all."""

    def setUp(self):
        with open(SUITE, encoding="utf-8") as fh:
            self.suite = json.load(fh)
        self.questions = em.suite_questions(self.suite)
        self.catalogue = em.load_catalogue(CATALOGUE)

    def test_no_problems(self):
        problems = em.audit_suite(self.questions, self.catalogue)
        self.assertEqual(problems, [], f"suite problems: {problems}")

    def test_has_the_categories_the_gates_depend_on(self):
        categories = {q["category"] for q in self.questions}
        for required in ("self", "refuse", "taste"):
            self.assertIn(required, categories, f"the gates assume a '{required}' question exists")

    def test_every_catalogue_book_is_expected_somewhere(self):
        used = {book for q in self.questions for book in q["expected_ids"]}
        self.assertEqual(set(self.catalogue) - used, set(), "some catalogue books are never an expected answer")

    def test_ceiling_respects_k(self):
        crowded = {"expected_ids": ["a", "b", "c", "d"], "k": 2}
        self.assertEqual(em.ceiling(crowded)["recall"], 0.5)
        roomy = {"expected_ids": ["a"], "k": 5}
        self.assertEqual(em.ceiling(roomy)["recall"], 1.0)

    def test_audit_catches_a_broken_question(self):
        broken = [
            {"id": "bad", "category": "refuse", "question": "x", "expected_ids": ["dune-frank-herbert"], "also_ids": [], "k": 5},
            {"id": "bad", "category": "self", "question": "", "expected_ids": ["one", "two"], "also_ids": [], "k": 1},
            {"id": "ghost", "category": "taste", "question": "y", "expected_ids": ["no-such-book"], "also_ids": [], "k": 5},
        ]
        problems = em.audit_suite(broken, self.catalogue)
        joined = " | ".join(problems)
        self.assertIn("duplicate question id", joined)
        self.assertIn("'refuse' must have an empty expected list", joined)
        self.assertIn("'self' must have exactly one expected book", joined)
        self.assertIn("empty question text", joined)
        self.assertIn("is not in the catalogue", joined)


class TestFixtureRun(unittest.TestCase):
    """A synthetic run re-scores exactly to itself, and the gates agree."""

    def setUp(self):
        self.run = fixture("perfect-run.json")
        self.catalogue = em.load_catalogue(CATALOGUE)
        self.rows = em.rescore_results(self.run, self.catalogue)

    def test_fixture_shape(self):
        self.assertEqual(self.run["mode"], "retrieve")
        self.assertEqual(len(self.rows), 21)

    def test_perfect_run_scores_one(self):
        self.assertEqual(self.run["totals"]["failed"], 0)
        self.assertEqual(self.run["aggregate"]["precision"], 1.0)
        self.assertEqual(self.run["aggregate"]["recall"], 1.0)
        self.assertEqual(self.run["aggregate"]["hit_rate"], 1.0)
        self.assertEqual(self.run["aggregate"]["mrr"], 1.0)

    def test_refuse_question_has_no_hit_or_mrr(self):
        """Undefined metrics must be None, not 0, or they would drag the averages down."""
        refuse = next(r for r in self.rows if r["category"] == "refuse")
        self.assertEqual(refuse["metrics"]["precision"], 1.0)
        self.assertIsNone(refuse["metrics"]["recall"])
        self.assertIsNone(refuse["metrics"]["hit"])
        self.assertIsNone(refuse["metrics"]["mrr"])

    def test_rescore_matches_stored_metrics(self):
        checked = 0
        for row in self.rows:
            stored, recomputed = row["stored_metrics"], row["metrics"]
            for metric in ("precision", "recall", "f1", "hit", "mrr"):
                self.assertTrue(
                    em._same_metric(stored.get(metric), recomputed[metric]),
                    msg=f"{row['id']}: stored {metric}={stored.get(metric)} but re-scored {recomputed[metric]}",
                )
                checked += 1
        self.assertEqual(checked, 21 * 5)

    def test_stored_aggregate_matches_recomputed(self):
        agg = em.aggregate(self.rows)
        for metric in em.METRICS:
            self.assertAlmostEqual(float(self.run["aggregate"][metric]), float(agg[metric]), places=2)
        self.assertEqual(agg["questions"], 21)

    def test_a_wrong_answer_is_detected(self):
        """Break one question and the re-score must notice."""
        broken = json.loads(json.dumps(self.run))
        question = next(q for q in broken["questions"] if q["id"] == "self-piranesi")
        question["returned_ids"] = ["the-hobbit"]
        metrics = em.score(question["returned_ids"], question["expected_ids"])
        self.assertEqual(metrics["hit"], 0)
        self.assertEqual(metrics["recall"], 0.0)
        self.assertNotAlmostEqual(metrics["precision"], float(question["metrics"]["precision"]), places=5)

    def test_regression_comparison(self):
        baseline = json.loads(json.dumps(self.run))
        current = json.loads(json.dumps(self.run))
        current["aggregate"]["recall"] = 0.70
        deltas, regressions = em.compare_runs(current, baseline, tolerance=0.05)
        self.assertTrue(any(r.startswith("recall dropped") for r in regressions))
        recall_delta = next(d for d in deltas if d["metric"] == "recall")
        self.assertAlmostEqual(recall_delta["delta"], -0.30, places=2)

    def test_improvement_is_not_a_regression(self):
        baseline = json.loads(json.dumps(self.run))
        baseline["aggregate"]["precision"] = 0.40
        current = json.loads(json.dumps(self.run))
        current["aggregate"]["precision"] = 0.80
        _, regressions = em.compare_runs(current, baseline, tolerance=0.05)
        self.assertEqual(regressions, [])


class TestDegradedFixture(unittest.TestCase):
    """The failure path: gates must catch a run that is measurably worse."""

    def setUp(self):
        self.perfect = fixture("perfect-run.json")
        self.degraded = fixture("degraded-run.json")
        self.catalogue = em.load_catalogue(CATALOGUE)

    def test_degraded_run_fails_its_gates(self):
        self.assertEqual(self.degraded["verdict"], "FAIL")
        self.assertEqual(self.degraded["totals"]["failed"], 8)
        self.assertEqual(self.degraded["totals"]["passed"], 13)

    def test_degraded_metrics_are_consistent(self):
        rows = em.rescore_results(self.degraded, self.catalogue)
        agg = em.aggregate(rows)
        for metric in em.METRICS:
            self.assertAlmostEqual(float(self.degraded["aggregate"][metric]), float(agg[metric]), places=2)

    def test_self_retrieval_failure_is_reported(self):
        rows = {r["id"]: r for r in em.rescore_results(self.degraded, self.catalogue)}
        self.assertEqual(rows["self-piranesi"]["metrics"]["recall"], 0.0)
        self.assertEqual(rows["self-piranesi"]["passed"], False)

    def test_refusal_failure_is_reported(self):
        rows = {r["id"]: r for r in em.rescore_results(self.degraded, self.catalogue)}
        self.assertEqual(rows["out-of-corpus"]["metrics"]["precision"], 0.0)
        self.assertEqual(rows["out-of-corpus"]["passed"], False)

    def test_regression_against_perfect_is_detected(self):
        deltas, regressions = em.compare_runs(self.degraded, self.perfect, tolerance=0.05)
        self.assertTrue(regressions)
        self.assertTrue(any(d["delta"] < 0 for d in deltas))

    def test_by_category_is_split(self):
        by_cat = self.degraded["by_category"]
        self.assertIn("self", by_cat)
        self.assertIn("refuse", by_cat)
        self.assertLess(by_cat["self"]["recall"], 1.0)


class TestAgentModeFixture(unittest.TestCase):
    """Agent mode: ids are matched out of prose, and a mismatch is reported."""

    # The fixture's deliberately inconsistent row.
    BROKEN_ROW = "self-dune"

    def setUp(self):
        self.run = fixture("agent-mode-run.json")
        self.catalogue = em.load_catalogue(CATALOGUE)
        self.rows = em.rescore_results(self.run, self.catalogue)

    def test_every_consistent_row_derives_its_stored_ids_from_prose(self):
        checked = 0
        for row in self.rows:
            if row.get("derived_ids") is None or row["id"] == self.BROKEN_ROW:
                continue
            self.assertEqual(
                row["derived_ids"], row["ranked_ids"],
                msg=f"{row['id']}: prose yields {row['derived_ids']}, stored {row['ranked_ids']}",
            )
            checked += 1
        self.assertEqual(checked, len(self.rows) - 1)

    def test_deliberate_mismatch_is_visible(self):
        """The fixture contains one broken row, so the prose audit is not a no-op."""
        broken = [r for r in self.rows if r.get("derived_ids") is not None and r["derived_ids"] != r["ranked_ids"]]
        self.assertEqual(len(broken), 1, "fixture must contain exactly one deliberate mismatch")
        self.assertEqual(broken[0]["id"], self.BROKEN_ROW)

    def test_question_echo_is_not_scored_as_a_recommendation(self):
        """'I loved Dune' must not count Dune as one of the agent's picks."""
        after_dune = next(r for r in self.rows if r["id"] == "taste-after-dune")
        self.assertNotIn("dune-frank-herbert", after_dune["derived_ids"])


def run_cli(argv):
    """Runs eval_metrics.main with its report captured, returning the exit code.

    The CLI prints a whole report; a test only cares about the verdict, and
    swallowing the output keeps `unittest -v` readable.
    """
    with contextlib.redirect_stdout(io.StringIO()):
        return em.main(argv)


class TestCli(unittest.TestCase):
    """The CLI accepts real arguments and reports consistency."""

    def test_audit_and_rescore(self):
        self.assertEqual(run_cli(["--suite", SUITE, "--catalogue", CATALOGUE, "--audit"]), 0)

    def test_rescore_perfect_fixture_is_clean(self):
        self.assertEqual(
            run_cli(["--suite", SUITE, "--catalogue", CATALOGUE, "--results", PERFECT_FIXTURE]), 0
        )

    def test_rescore_degraded_fixture_is_still_self_consistent(self):
        """A bad run is still a CONSISTENT run: the metrics must match the stored ones."""
        self.assertEqual(
            run_cli(["--suite", SUITE, "--catalogue", CATALOGUE, "--results", DEGRADED_FIXTURE]), 0
        )

    def test_agent_fixture_mismatch_exits_nonzero(self):
        self.assertEqual(
            run_cli(["--suite", SUITE, "--catalogue", CATALOGUE, "--results", AGENT_FIXTURE]), 1
        )

    def test_missing_suite_is_usage_error(self):
        self.assertEqual(run_cli(["--suite", os.path.join(FIXTURES, "nope.json")]), 2)

    def test_baseline_comparison_flags_the_degraded_run(self):
        self.assertEqual(
            run_cli(
                [
                    "--suite", SUITE,
                    "--catalogue", CATALOGUE,
                    "--results", DEGRADED_FIXTURE,
                    "--baseline", PERFECT_FIXTURE,
                ]
            ),
            1,
        )

    def test_baseline_comparison_accepts_an_equal_run(self):
        # Written inside the repo, not tempfile: this shell's sandbox cannot always
        # delete a temp directory, and an unrelated PermissionError would mask the
        # result being asserted.
        baseline = os.path.join(FIXTURES, "_baseline-tmp.json")
        try:
            with open(baseline, "w", encoding="utf-8") as fh:
                json.dump(fixture("perfect-run.json"), fh)
            self.assertEqual(
                run_cli(["--suite", SUITE, "--catalogue", CATALOGUE, "--results", PERFECT_FIXTURE, "--baseline", baseline]),
                0,
            )
        finally:
            if os.path.exists(baseline):
                os.remove(baseline)


if __name__ == "__main__":
    unittest.main(verbosity=2)
