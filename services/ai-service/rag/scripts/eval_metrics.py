#!/usr/bin/env python3
"""eval_metrics.py - re-score a saved RAG evaluation and audit the metric logic.

Why this exists next to scripts/eval-rag.ps1, which already computes the same
numbers in line:

  1. audit       - the suite itself can be checked with no database, no network
                   and no API key. `-ValidateOnly` in PowerShell does this too;
                   this is the second, independent implementation, so a mistake
                   in one is visible in the other.
  2. re-score    - a saved results JSON is re-scored from its raw returned ids.
                   If the numbers move, the harness was wrong, not the library.
  3. compare     - two runs can be compared without re-running anything.
  4. expected    - prints the CEILING of every question (the best score it could
                   possibly reach), which is how a threshold is chosen instead of
                   guessed.

The metric definitions are duplicated from scripts/eval-rag.ps1 ON PURPOSE and
must not drift. `tests/test_eval_metrics.py` runs offline and proves they agree
with a checked-in fixture.

Usage:
    python scripts/eval_metrics.py --suite evals/rag-eval-suite.json --audit
    python scripts/eval_metrics.py --suite evals/rag-eval-suite.json --expected
    python scripts/eval_metrics.py --suite evals/rag-eval-suite.json \
        --results evals/results/rag-eval-20261003-101500-retrieve.json --detail
    python scripts/eval_metrics.py --results <run-a.json> --baseline <run-b.json>

Exit code 0 = consistent, 1 = a discrepancy or a failed audit, 2 = bad usage.
"""

from __future__ import annotations

import argparse
import glob
import json
import os
import re
import sys

PROJECT_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DEFAULT_SUITE = os.path.join(PROJECT_ROOT, "evals", "rag-eval-suite.json")
DEFAULT_RESULTS_DIR = os.path.join(PROJECT_ROOT, "evals", "results")
DEFAULT_CATALOGUE = os.path.join(PROJECT_ROOT, "samples", "books.csv")

CATEGORIES = ("self", "taste", "genre", "author", "constraint", "refuse")
METRICS = ("precision", "recall", "f1", "hit_rate", "mrr")

# Questions whose text names the book it is looking for are supposed to: that is
# what category 'self' tests. Everywhere else, an echo of the question is not an
# answer, so the suite must declare such ids under 'also_mentions'.
SELF_CATEGORY = "self"


# ---------------------------------------------------------------------------
# Loading
# ---------------------------------------------------------------------------

def load_json(path):
    with open(path, encoding="utf-8") as fh:
        return json.load(fh)


def find_latest_results(directory=DEFAULT_RESULTS_DIR):
    """Newest rag-eval-*.json under evals/results, or None."""
    if not os.path.isdir(directory):
        return None
    candidates = sorted(glob.glob(os.path.join(directory, "rag-eval-*.json")))
    return candidates[-1] if candidates else None


def load_catalogue(path=DEFAULT_CATALOGUE):
    """book_id -> title, from the sample CSV. Used only for display and audits."""
    import csv

    if not os.path.exists(path):
        return {}
    with open(path, encoding="utf-8", newline="") as fh:
        return {row["book_id"].strip(): row["title"].strip() for row in csv.DictReader(fh)}


def suite_questions(suite):
    """Flattens a suite into per-question dicts with defaults applied."""
    defaults = suite.get("defaults") or {}
    default_k = int(defaults.get("k", 5))
    default_category = defaults.get("category", "taste")

    out = []
    for index, q in enumerate(suite.get("questions") or [], start=1):
        out.append(
            {
                "index": index,
                "id": q.get("id", f"question-{index}"),
                "category": q.get("category", default_category),
                "question": q.get("question", ""),
                "expected_ids": [b for b in (q.get("expected") or []) if b],
                "also_ids": [b for b in (q.get("also_mentions") or []) if b],
                "k": int(q.get("k", default_k)),
                "note": q.get("note", ""),
            }
        )
    return out


# ---------------------------------------------------------------------------
# Title matching (agent mode: the answer is prose, not ids)
# ---------------------------------------------------------------------------

def normalise_title(title):
    """'Thinking, Fast and Slow' -> 'thinking fast and slow' (leading article dropped).

    Mirrors Get-NormalisedTitle in scripts/eval-rag.ps1.
    """
    keep = []
    for ch in (title or "").lower():
        keep.append(ch if ch.isalnum() else " ")
    text = " ".join("".join(keep).split())
    for article in ("the ", "a ", "an "):
        if text.startswith(article):
            text = text[len(article):]
            break
    return text.strip()


def title_map(catalogue):
    """normalised title -> [book_id, ...] (longest key first when scanning)."""
    mapping = {}
    for book_id, title in catalogue.items():
        key = normalise_title(title)
        if key:
            mapping.setdefault(key, []).append(book_id)
    return mapping


def mentioned_books(text, catalogue):
    """Book ids whose normalised title appears in `text`, in order of first mention.

    Mirrors Get-BookIdsMentioned in scripts/eval-rag.ps1. Every rule below is
    load-bearing, and tests/test_eval_metrics.py pins each one:

      * longest title first, so a shorter title cannot shadow a longer one
      * a full title must appear complete: every one of its words, in order
      * a TRUNCATED title is accepted as a prefix - 'Sapiens is the obvious pick'
        matches 'Sapiens: A Brief History of Humankind', because an answer rarely
        repeats a subtitle and demanding it would miss a real recommendation.
      * KNOWN COST of that rule, accepted deliberately: the words of a truncated
        title need not be adjacent, so 'The quiet city slept' also matches
        'The City and the City'. Matching the full title always works; the cost is
        only that a partial title is loose. It errs toward crediting a book the
        agent really did name, and 'self-dune'-style questions, where a false
        positive would matter, ask for whole titles.
    """
    if not text:
        return []

    mapping = title_map(catalogue)
    flat = " " + " ".join("".join(c if c.isalnum() else " " for c in text.lower()).split()) + " "

    # Collect every title's first position, longest title first so shadowing cannot
    # happen, then report in TEXT order so the ranking reflects the order the answer
    # actually mentioned the books.
    hits = {}
    for key in sorted(mapping, key=len, reverse=True):
        if len(key) < 3:
            continue
        # The title's words in order, each later word optional (a truncated title),
        # then one trailing word boundary.
        #   sapiens(?: a)?(?: brief)?(?: history)?(?: of)?(?: humankind)?(?![a-z0-9])
        # matches "Sapiens is the obvious pick" and the full subtitle form, while
        # "The quiet city slept" does NOT match "the city and the city" because a
        # skipped word leaves the next one unmatched.
        words = [re.escape(word) for word in key.split(" ")]
        pattern = words[0] + "".join("(?:" + word + ")?" for word in words[1:]) + "(?![a-z0-9])"
        match = re.search(pattern, flat)
        if not match:
            continue
        for book_id in mapping[key]:
            if book_id not in hits:
                hits[book_id] = match.start()

    return [book_id for book_id, _ in sorted(hits.items(), key=lambda item: item[1])]


def extract_mentioned_ids(question, answer_text, catalogue, also_ids=()):
    """Book ids the agent actually named, from its prose answer.

    Two exclusions, both essential:
      * `also_ids` - the suite's explicit "the question mentions this book" list
      * any title that appears in the QUESTION itself. An answer that echoes
        "Dune" back because the user said "I loved Dune" has recommended nothing,
        and without this the echo would be scored as a hit.
    """
    mentioned = mentioned_books(answer_text, catalogue)
    question_ids = set(mentioned_books(question, catalogue))
    excluded = set(also_ids) | question_ids
    return [b for b in mentioned if b not in excluded]


# ---------------------------------------------------------------------------
# Metrics - the single definition, mirroring Get-MetricSet in eval-rag.ps1
# ---------------------------------------------------------------------------

def score(ranked_ids, expected_ids):
    """Scores one question. Exact mirror of Get-MetricSet in scripts/eval-rag.ps1.

    Duplicates are removed BEFORE scoring, so a recommender that repeats one book
    is not rewarded for it. A question with no expected book ('refuse') is scored
    on precision alone: the correct answer names nothing, so precision is 1.0 when
    nothing was named and 0.0 otherwise, and recall is fixed at 1.0 because it is
    undefined and must not gate the question.
    """
    ranked, expected = list(ranked_ids or []), list(expected_ids or [])
    if len(set(ranked)) != len(ranked):
        deduped, seen = [], set()
        for item in ranked:
            if item not in seen:
                seen.add(item)
                deduped.append(item)
        ranked = deduped

    expected_set = set(expected)
    hits = [b for b in ranked if b in expected_set]

    # A "refuse" question has no expected book: precision is the whole story (the
    # correct answer names nothing), recall is meaningless, and hit/MRR are not
    # defined either. They are reported as None and excluded from the averages
    # rather than being counted as failures.
    if expected:
        precision = len(hits) / len(ranked) if ranked else 0.0
        recall = len(hits) / len(expected)
        hit = 1 if hits else 0
    else:
        precision = 1.0 if not ranked else 0.0
        recall = None
        hit = None

    # recall is None only for a 'refuse' question, where F1 has no meaning either.
    f1 = (2 * precision * recall / (precision + recall)) if (recall is not None and (precision + recall)) else None
    first_rank = 0
    for position, book_id in enumerate(ranked, start=1):
        if book_id in expected_set:
            first_rank = position
            break

    return {
        "precision": round(precision, 2),
        "recall": round(recall, 2) if recall is not None else None,
        "f1": round(f1, 2) if f1 is not None else None,
        "hit": hit,
        "mrr": round(1.0 / first_rank, 2) if first_rank else (0.0 if expected else None),
        "rank_first_hit": first_rank,
        "true_positives": len(hits),
        "returned": len(ranked),
        "expected_count": len(expected),
        "relevant_ranked": hits,
        "missing": [b for b in expected if b not in set(ranked)],
    }


def aggregate(rows):
    """Macro-average over per-question metrics: every question counts once.

    Per-question rows carry `hit` (0 or 1); the aggregate reports it as
    `hit_rate`, which is the mean of those hits. Both spellings are accepted here
    so the same function can average a run's stored aggregate rows too.

    Undefined values (None - a 'refuse' question has no hit and no MRR) are
    EXCLUDED from the average and its denominator, so a refusal check neither
    inflates nor deflates the retrieval metrics.
    """
    rows = list(rows)
    empty = {m: 0.0 for m in METRICS}
    empty["questions"] = 0
    empty["passed"] = 0
    empty["failed"] = 0
    if not rows:
        return empty

    def raw(row, metric):
        metrics = row["metrics"]
        if metric in metrics:
            return metrics[metric]
        alias = "hit" if metric == "hit_rate" else metric
        if alias in metrics:
            return metrics[alias]
        raise KeyError(f"metric '{metric}' is missing from {row.get('id')}")

    out = {}
    for metric in METRICS:
        values = [raw(r, metric) for r in rows]
        known = [float(v) for v in values if v is not None]
        out[metric] = round(sum(known) / len(known), 2) if known else 0.0

    out["questions"] = len(rows)
    out["passed"] = sum(1 for r in rows if r.get("passed"))
    out["failed"] = sum(1 for r in rows if not r.get("passed"))
    return out


def aggregate_by_category(rows):
    out = {}
    for category in CATEGORIES:
        subset = [r for r in rows if r.get("category") == category]
        if subset:
            out[category] = aggregate(subset)
    return out


# ---------------------------------------------------------------------------
# Audits - everything checkable with no network and no database
# ---------------------------------------------------------------------------

def audit_suite(questions, catalogue):
    """Returns a list of problem strings. Empty means the suite is coherent."""
    problems = []
    seen = {}

    for q in questions:
        label = q["id"]

        if label in seen:
            problems.append(f"{label}: duplicate question id")
        seen[label] = True

        if not q["question"].strip():
            problems.append(f"{label}: empty question text")
        if q["category"] not in CATEGORIES:
            problems.append(f"{label}: unknown category '{q['category']}'")
        if q["k"] < 1:
            problems.append(f"{label}: k must be >= 1")
        if q["category"] == "refuse" and q["expected_ids"]:
            problems.append(f"{label}: 'refuse' must have an empty expected list")
        if q["category"] != "refuse" and not q["expected_ids"]:
            problems.append(f"{label}: category '{q['category']}' needs at least one expected book")
        if q["category"] == "self" and len(q["expected_ids"]) != 1:
            problems.append(f"{label}: 'self' must have exactly one expected book")
        if len(q["expected_ids"]) != len(set(q["expected_ids"])):
            problems.append(f"{label}: duplicate ids in 'expected'")
        if set(q["expected_ids"]) & set(q["also_ids"]):
            problems.append(
                f"{label}: {sorted(set(q['expected_ids']) & set(q['also_ids']))} is both expected and also_mentions"
            )
        if len(q["expected_ids"]) > q["k"]:
            problems.append(
                f"{label}: {len(q['expected_ids'])} expected books but k={q['k']} - recall@k is capped at "
                f"{round(q['k'] / len(q['expected_ids']), 2)}"
            )

        if catalogue:
            for book_id in q["expected_ids"] + q["also_ids"]:
                if book_id not in catalogue:
                    problems.append(f"{label}: id '{book_id}' is not in the catalogue")

        if q["category"] != SELF_CATEGORY and catalogue:
            for book_id in q["expected_ids"]:
                title = normalise_title(catalogue.get(book_id, ""))
                if title and title in normalise_title(q["question"]) and book_id not in q["also_ids"]:
                    problems.append(
                        f"{label}: the question names '{book_id}' but it is not in 'also_mentions', "
                        "so an echo of the question would count as a hit"
                    )
    return problems


def ceiling(question):
    """The best scores a question could reach, given k and its expected set."""
    expected = question["expected_ids"]
    k = question["k"]
    if not expected:
        return {"precision": 1.0, "recall": 1.0, "note": "refuse: perfect means naming nothing"}
    if len(expected) > k:
        best_recall = round(k / len(expected), 2)
        best_precision = 1.0
        note = f"k={k} < {len(expected)} expected books: recall@k cannot exceed {best_recall}"
    else:
        best_recall = 1.0
        best_precision = 1.0
        note = "achievable in full"
    return {"precision": best_precision, "recall": best_recall, "note": note}


def rescore_results(results, catalogue):
    """Re-scores every question in a saved results file from its raw returned ids."""
    rows = []
    for q in results.get("questions") or []:
        if q.get("error"):
            rows.append({"id": q.get("id"), "category": q.get("category"), "error": q["error"], "metrics": None})
            continue

        ranked = list(q.get("returned_ids") or [])

        # Agent mode stores prose. Re-deriving the ids from that prose and diffing
        # against the stored ids catches a matching bug in the harness itself.
        derived = None
        if q.get("mode") == "agent" and q.get("answer_text") is not None and catalogue:
            derived = extract_mentioned_ids(
                q.get("question", ""), q["answer_text"], catalogue, q.get("also_ids") or []
            )[: int(q.get("k") or 0)]

        rows.append(
            {
                "id": q.get("id"),
                "category": q.get("category"),
                "k": q.get("k"),
                "expected_ids": q.get("expected_ids") or [],
                "ranked_ids": ranked,
                "derived_ids": derived,
                "stored_metrics": q.get("metrics") or {},
                "metrics": score(ranked, q.get("expected_ids") or []),
                "passed": q.get("passed"),
            }
        )
    return rows


def compare_runs(current, baseline, tolerance=0.05):
    """Metric deltas between two runs. Regression = a drop beyond `tolerance`."""
    deltas, regressions = [], []
    for metric in METRICS:
        was = float((baseline.get("aggregate") or {}).get(metric, 0.0))
        now = float((current.get("aggregate") or {}).get(metric, 0.0))
        delta = round(now - was, 2)
        deltas.append({"metric": metric, "was": was, "now": now, "delta": delta})
        if delta < -tolerance:
            regressions.append(f"{metric} dropped {abs(delta)} (was {was}, now {now})")
    return deltas, regressions


# ---------------------------------------------------------------------------
# Reporting
# ---------------------------------------------------------------------------

def fmt(value):
    return "-" if value is None else value


def _same_metric(stored, recomputed):
    """Compares one stored metric with a re-scored one. None == None is a match:
    'refuse' questions have no hit and no MRR by definition, and that is correct."""
    if stored is None and recomputed is None:
        return True
    if stored is None or recomputed is None:
        return False
    try:
        return abs(float(stored) - float(recomputed)) < 0.005
    except (TypeError, ValueError):
        return False


def print_audit(questions, catalogue, problems, warnings):
    print(f"suite      : {len(questions)} question(s)")
    print(f"catalogue  : {len(catalogue)} book(s)" if catalogue else "catalogue  : not available")
    per_category = {}
    for q in questions:
        per_category[q["category"]] = per_category.get(q["category"], 0) + 1
    print("categories : " + ", ".join(f"{c} {n}" for c, n in sorted(per_category.items())))

    used = {b for q in questions for b in q["expected_ids"]}
    if catalogue:
        unused = sorted(set(catalogue) - used)
        print(f"coverage   : {len(used)}/{len(catalogue)} catalogue books are an expected answer")
        if unused:
            warnings.append(f"{len(unused)} catalogue book(s) are never expected: {', '.join(unused)}")

    for w in warnings:
        print(f"  WARN {w}")
    for p in problems:
        print(f"  PROBLEM {p}")
    print("AUDIT " + ("PASS" if not problems else f"FAIL ({len(problems)} problem(s))"))


def print_expected(questions, catalogue):
    print(f"{'id':<30} {'cat':<10} {'k':>2} {'exp':>3}  best precision / recall   note")
    print("-" * 110)
    for q in questions:
        best = ceiling(q)
        note = q["note"] or best["note"]
        if len(note) > 46:
            note = note[:43] + "..."
        print(
            f"{q['id']:<30} {q['category']:<10} {q['k']:>2} {len(q['expected_ids']):>3}  "
            f"{best['precision']:>5} / {best['recall']:<5}          {note}"
        )


def print_rescore(rows, catalogue, detail=False):
    """Prints the re-score table and returns a list of discrepancies (empty = clean)."""
    diffs = []
    print(f"{'id':<30} {'cat':<10} {'prec':>5} {'rec':>5} {'hit':>4}  stored        match")
    print("-" * 96)
    for row in rows:
        if row.get("error"):
            print(f"{row['id']:<30} {str(row.get('category')):<10} {'-':>5} {'-':>5} {'-':>4}  ERROR {row['error']}")
            continue

        metrics = row["metrics"]
        stored = row["stored_metrics"]
        same = all(_same_metric(stored.get(m), metrics[m]) for m in ("precision", "recall", "hit", "mrr"))
        mismatch = "" if same else "MISMATCH"
        if not same:
            diffs.append(f"{row['id']}: stored {stored} != re-scored {metrics}")
        if row.get("derived_ids") is not None and row["derived_ids"] != row["ranked_ids"]:
            diffs.append(
                f"{row['id']}: agent answer text yields {row['derived_ids']} but the run stored {row['ranked_ids']}"
            )
            mismatch = (mismatch + " ID-DIFF").strip()

        titles = ", ".join(catalogue.get(b, b) for b in row["ranked_ids"][:3]) or "(nothing)"
        stored_pair = f"{fmt(stored.get('precision'))}/{fmt(stored.get('recall'))}"
        print(
            f"{row['id']:<30} {row['category']:<10} {fmt(metrics['precision']):>5} {fmt(metrics['recall']):>5} "
            f"{fmt(metrics['hit']):>4}  {stored_pair:<13} {mismatch}"
        )
        if detail:
            print(f"    returned: {titles}")
            print(f"    expected: {', '.join(catalogue.get(b, b) for b in row['expected_ids']) or '(nothing)'}")
            if metrics["missing"]:
                print(f"    missing : {', '.join(catalogue.get(b, b) for b in metrics['missing'])}")

    scored = [r for r in rows if r.get("metrics")]
    agg = aggregate(scored)
    print("-" * 96)
    print("re-scored aggregate: " + "  ".join(f"{m} {agg[m]}" for m in METRICS))
    for category, values in aggregate_by_category(scored).items():
        print(f"  {category:<12} questions {values['questions']:<3} prec {values['precision']:<5} rec {values['recall']:<5} hit {values['hit_rate']}")
    return diffs


def main(argv=None):
    parser = argparse.ArgumentParser(description="Re-score and audit the Book RAG evaluation.")
    parser.add_argument("--suite", default=DEFAULT_SUITE, help="evaluation suite JSON")
    parser.add_argument("--results", help="a results JSON written by scripts/eval-rag.ps1 (default: newest under evals/results)")
    parser.add_argument("--baseline", help="compare --results against this results JSON")
    parser.add_argument("--catalogue", default=DEFAULT_CATALOGUE, help="books CSV, for titles and id checks")
    parser.add_argument("--audit", action="store_true", help="check the suite for internal contradictions")
    parser.add_argument("--expected", action="store_true", help="print each question's score ceiling")
    parser.add_argument("--detail", action="store_true", help="print returned and missing books per question")
    parser.add_argument("--tolerance", type=float, default=0.05, help="allowed metric drop before it is a regression")
    args = parser.parse_args(argv)

    if not os.path.exists(args.suite):
        print(f"FAIL: suite not found: {args.suite}")
        return 2
    suite = load_json(args.suite)
    questions = suite_questions(suite)
    catalogue = load_catalogue(args.catalogue)

    # Default action when nothing was asked for: audit, and re-score if possible.
    if not (args.audit or args.expected or args.results or args.baseline):
        args.audit = True

    exit_code = 0
    warnings = []

    if args.audit or args.expected:
        problems = audit_suite(questions, catalogue)
        if args.expected:
            print_expected(questions, catalogue)
            print()
        if args.audit:
            print_audit(questions, catalogue, problems, warnings)
            if problems:
                exit_code = 1
        elif problems:
            for p in problems:
                print(f"  PROBLEM {p}")
            exit_code = 1

    results_path = args.results
    if not results_path and args.baseline is None and not (args.audit or args.expected):
        results_path = find_latest_results()

    if results_path or args.baseline:
        if not results_path:
            results_path = find_latest_results()
        if not results_path or not os.path.exists(results_path):
            print(f"FAIL: no results file found ({results_path})")
            return 2

        results = load_json(results_path)
        print(f"\nresults    : {results_path}")
        print(f"run        : {results.get('started_at')} mode={results.get('mode')} k={results.get('k')} verdict={results.get('verdict')}")
        rows = rescore_results(results, catalogue)
        differences = print_rescore(rows, catalogue, detail=args.detail)
        if differences:
            print("FAIL: the saved results do not match a re-score - the harness metric code and this file disagree")
            exit_code = 1
        else:
            print("OK: re-scored metrics match the stored ones")

        if args.baseline:
            if not os.path.exists(args.baseline):
                print(f"FAIL: baseline not found: {args.baseline}")
                return 2
            baseline = load_json(args.baseline)
            deltas, regressions = compare_runs(results, baseline, args.tolerance)
            print(f"\nbaseline   : {args.baseline} ({baseline.get('started_at')})")
            for d in deltas:
                print(f"  {d['metric']:<10} {d['was']:>6} -> {d['now']:>6}  ({d['delta']:+.2f})")
            for r in regressions:
                print(f"  REGRESSION {r}")
            if regressions:
                exit_code = 1

    return exit_code


if __name__ == "__main__":
    sys.exit(main())
