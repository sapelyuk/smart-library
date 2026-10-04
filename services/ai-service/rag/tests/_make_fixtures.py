#!/usr/bin/env python3
"""make_fixtures.py - regenerate the checked-in eval fixtures.

The fixtures let the scoring logic be tested offline: no database, no API key, no
Gemini quota, no VPN. They are SYNTHETIC (answers are derived from the suite's own
expected books), so they prove the SCORING, never the quality of the library.

  evals/fixtures/perfect-run.json    every question returns exactly its expected
                                     books, refusing questions name nothing
  evals/fixtures/degraded-run.json   the same run with realistic failures: some
                                     expected books missing, some questions
                                     answered with a wrong book, one self missed.
                                     Used to prove the gates and the report.
  evals/fixtures/agent-mode-run.json prose answers instead of ids, plus ONE
                                     deliberately wrong stored answer (self-dune)
                                     so the "re-derive ids from the prose" audit
                                     cannot pass by accident.

Every metric in every fixture is computed by scripts/eval_metrics.py, so a fixture
and the scorer can never disagree by accident.

    python tests/make_fixtures.py
"""

from __future__ import annotations

import json
import os
import sys

PROJECT_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(PROJECT_ROOT, "scripts"))

import eval_metrics as em  # noqa: E402

SUITE = os.path.join(PROJECT_ROOT, "evals", "rag-eval-suite.json")
CATALOGUE = os.path.join(PROJECT_ROOT, "samples", "books.csv")
FIXTURES = os.path.join(PROJECT_ROOT, "evals", "fixtures")
RUN_STAMP = "2026-10-02 18:00:00"

# A plausible wrong answer used by the degraded fixture, chosen to be in the same
# genre as the questions it spoils so the run looks like a real retrieval miss
# rather than nonsense.
FILLER = "the-hobbit"

# The one agent-mode row whose stored ids disagree with its own prose.
BROKEN_ROW = "self-dune"

# How the degraded run fails. Keys are question ids from the suite.
DEGRADED = {
    "taste-after-dune": {"returned": ["neuromancer-william-gibson", FILLER], "drop": ["left-hand-of-darkness", "the-city-and-the-city"]},
    "taste-le-guin": {"returned": ["the-hobbit"], "drop": ["left-hand-of-darkness"]},
    "self-piranesi": {"returned": ["the-hobbit"], "drop": ["piranesi"]},
    "taste-short-and-funny": {"returned": ["the-hobbit", "never-let-me-go"], "drop": ["educating-rita"]},
    "taste-classic-detective": {"returned": ["the-big-sleep", "gone-girl"], "drop": ["the-hound-of-the-baskervilles"]},
    "genre-mystery": {"returned": ["the-big-sleep", "gone-girl"], "drop": ["the-hound-of-the-baskervilles"]},
    "author-ishiguro": {"returned": ["never-let-me-go", "the-hobbit"], "drop": ["the-remains-of-the-day"]},
    "out-of-corpus": {"returned": ["the-hobbit"], "drop": []},
}


def build_rows(questions, catalogue, plan=None, mode="retrieve"):
    """One results row per question. `plan` maps question id -> overrides."""
    plan = plan or {}
    rows = []

    for q in questions:
        expected = q["expected_ids"]
        override = plan.get(q["id"], {})

        if "returned" in override:
            returned = list(override["returned"])
        elif q["category"] == "refuse":
            returned = []
        else:
            returned = expected[: q["k"]]

        dropped = set(override.get("drop", []))
        returned = [b for b in returned if b not in dropped]

        metrics = em.score(returned, expected)

        # Pass/fail is decided exactly like scripts/eval-rag.ps1 decides it, so
        # the fixture's gates and the harness's gates mean the same thing.
        reasons = []
        if q["category"] == "refuse":
            if metrics["precision"] < 1.0:
                reasons.append("refuse precision below 1.0 (it named a book for an out-of-catalogue request)")
        else:
            if metrics["precision"] < 0.40:
                reasons.append(f"precision@{q['k']} {metrics['precision']} < 0.4")
            if metrics["recall"] < 0.60:
                reasons.append(f"recall@{q['k']} {metrics['recall']} < 0.6")
            if metrics["hit"] != 1:
                reasons.append(f"no expected book in the top {q['k']}")
        if q["category"] == "self" and metrics["recall"] < 1.0:
            reasons.append(f"self-recall {metrics['recall']} < 1.0")

        rows.append(
            {
                "id": q["id"],
                "category": q["category"],
                "question": q["question"],
                "note": q["note"],
                "k": q["k"],
                "mode": mode,
                "expected_ids": expected,
                "expected_titles": [catalogue.get(b, b) for b in expected],
                "also_ids": q["also_ids"],
                "returned_ids": returned,
                "returned_titles": [catalogue.get(b, b) for b in returned],
                "scores": [round(max(0.1, 0.90 - 0.03 * i), 2) for i in range(len(returned))],
                "missing_titles": [catalogue.get(b, b) for b in metrics["missing"]],
                "answer_text": "",
                "metrics": metrics,
                "passed": not reasons,
                "reasons": reasons,
                "error": "",
            }
        )
    return rows


def build_run(rows, catalogue, mode, fixture_note, suite_name):
    scored = [r for r in rows if not r["error"]]
    return {
        "suite": suite_name,
        "suite_version": 1,
        "started_at": RUN_STAMP,
        "mode": mode,
        "k": 5,
        "embed_model": "models/gemini-embedding-001",
        "embed_dim": 3072,
        "base_url": "http://localhost:5678",
        "catalogue_source": f"samples\\books.csv ({len(catalogue)} rows)",
        "catalogue_count": len(catalogue),
        "questions": rows,
        "errors": 0,
        "totals": {
            "questions": len(rows),
            "scored": len(scored),
            "errors": 0,
            "passed": sum(1 for r in scored if r["passed"]),
            "failed": sum(1 for r in scored if not r["passed"]),
        },
        "aggregate": em.aggregate(scored),
        "by_category": em.aggregate_by_category(scored),
        "gates": [],
        "verdict": "PASS" if all(r["passed"] for r in scored) else "FAIL",
        "_fixture": fixture_note,
    }


def as_agent(run, catalogue):
    """Same run in agent mode: prose answers instead of ids.

    The stored ids are built by running the SAME extractor the audit uses over the
    prose, so the fixture is self-consistent by construction: the prose is written,
    then the ids are derived from it, never the other way round.
    """
    for row in run["questions"]:
        row["mode"] = "agent"
        names = [catalogue.get(b, b) for b in row["returned_ids"]]
        if not names:
            row["answer_text"] = "I could not find anything in the library that matches that."
        else:
            row["answer_text"] = "Here is what I found: " + " ".join(f"{n} - it matches what you described." for n in names)

        derived = em.extract_mentioned_ids(row["question"], row["answer_text"], catalogue, row["also_ids"])
        row["returned_ids"] = derived[: row["k"]]
        row["returned_titles"] = [catalogue.get(b, b) for b in row["returned_ids"]]
        row["metrics"] = em.score(row["returned_ids"], row["expected_ids"])

    run["mode"] = "agent"
    run["_fixture"] = (
        "SYNTHETIC agent-mode run generated by tests/make_fixtures.py, from the perfect run so that "
        f"only one thing is wrong: the row '{BROKEN_ROW}' stores ids that do not match its own answer_text. "
        "The re-score audit re-derives ids from the prose and must flag that row."
    )

    broken = next(r for r in run["questions"] if r["id"] == BROKEN_ROW)
    broken["returned_ids"] = [FILLER]
    broken["returned_titles"] = [catalogue.get(FILLER, FILLER)]
    broken["metrics"] = em.score(broken["returned_ids"], broken["expected_ids"])
    broken["passed"] = False

    scored = [r for r in run["questions"] if not r["error"]]
    run["totals"]["passed"] = sum(1 for r in scored if r["passed"])
    run["totals"]["failed"] = sum(1 for r in scored if not r["passed"])
    run["aggregate"] = em.aggregate(scored)
    run["by_category"] = em.aggregate_by_category(scored)
    run["verdict"] = "FAIL"
    return run


def write(name, run):
    path = os.path.join(FIXTURES, name)
    with open(path, "w", encoding="utf-8") as fh:
        json.dump(run, fh, indent=2, ensure_ascii=False)
        fh.write("\n")
    print(f"wrote evals/fixtures/{name}")
    return path


def main():
    with open(SUITE, encoding="utf-8") as fh:
        suite = json.load(fh)
    questions = em.suite_questions(suite)
    catalogue = em.load_catalogue(CATALOGUE)
    suite_name = suite.get("suite", "book-rag-core")

    problems = em.audit_suite(questions, catalogue)
    if problems:
        print("refusing to build fixtures from an incoherent suite:")
        for p in problems:
            print(f"  {p}")
        return 1

    os.makedirs(FIXTURES, exist_ok=True)

    perfect = build_run(
        build_rows(questions, catalogue, mode="retrieve"),
        catalogue,
        "retrieve",
        "SYNTHETIC perfect run generated by tests/make_fixtures.py. Answers are the suite's own "
        "expected books, so this fixture proves the SCORING, not the library.",
        suite_name,
    )
    write("perfect-run.json", perfect)

    degraded = build_run(
        build_rows(questions, catalogue, plan=DEGRADED, mode="retrieve"),
        catalogue,
        "retrieve",
        "SYNTHETIC degraded run generated by tests/make_fixtures.py: realistic failures (missing "
        "expected books, wrong books, one missed self-retrieval) used to prove the gates and the report.",
        suite_name,
    )
    write("degraded-run.json", degraded)

    agent = as_agent(
        build_run(
            build_rows(questions, catalogue, mode="agent"),
            catalogue,
            "agent",
            "placeholder",
            suite_name,
        ),
        catalogue,
    )
    write("agent-mode-run.json", agent)

    print(f"\nperfect  : {perfect['aggregate']} verdict={perfect['verdict']}")
    print(f"degraded : {degraded['aggregate']} verdict={degraded['verdict']} failed={degraded['totals']['failed']}")
    print(f"agent    : {agent['aggregate']} verdict={agent['verdict']} failed={agent['totals']['failed']}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
