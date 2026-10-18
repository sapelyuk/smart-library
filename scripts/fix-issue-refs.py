#!/usr/bin/env python3
"""Replace local issue refs #1..#15 in issue bodies with real numbers #89..#103.

SAFE: only replaces #N where N in 1..15 AND N is NOT in the keep-list of
real repo issues.  Idempotent: running twice produces no changes.

Usage:
    python scripts/fix-issue-refs.py          # dry-run (default)
    python scripts/fix-issue-refs.py --apply  # real edit via gh CLI
"""

import json
import re
import subprocess
import sys

# --- Mapping: local DAG number -> real GitHub issue number -----------------
# #1 -> #89, #2 -> #90, ...  #15 -> #103
LOCAL_TO_REAL = {n: 88 + n for n in range(1, 16)}

# Issues that exist in the repo and must NEVER be re-mapped.
# #6  containerization, #7 broker, #9 postgres-repo, #10 gateway, #14 events,
# #34 rate-limiting, #48 notification, #49 loan-service, #60 e2e,
# #89-#106 Phase 1, #108 PR (doc refs only), #109 infra.
KEEP = {6, 7, 9, 10, 14, 34, 48, 49, 60, 89, 90, 91, 92, 93, 94, 95,
        96, 97, 98, 99, 100, 101, 102, 103, 104, 105, 106, 108, 109}

# Issues to process (Phase 1a + 1b).
ISSUE_IDS = list(range(89, 107))


def gh(*args: str) -> str:
    r = subprocess.run(
        ["gh", *args], capture_output=True, check=True,
        encoding="utf-8", errors="replace",
    )
    return r.stdout


def fix_refs(body: str) -> tuple[str, list[tuple[int, int]]]:
    """Return (new_body, [(old, new), ...]) for every replacement made."""
    changes: list[tuple[int, int]] = []

    def repl(m: re.Match) -> str:
        n = int(m.group(1))
        if n in KEEP:
            return m.group(0)
        if n in LOCAL_TO_REAL:
            changes.append((n, LOCAL_TO_REAL[n]))
            return f"#{LOCAL_TO_REAL[n]}"
        return m.group(0)

    # Match #N where N is 1-2 digits and not part of a larger number.
    new_body = re.sub(r"(?<!\d)#(\d{1,2})(?!\d)", repl, body)
    return new_body, changes


def main(apply: bool) -> None:
    changed: list[int] = []
    for num in ISSUE_IDS:
        issue = json.loads(gh("issue", "view", str(num), "--json", "body"))
        body: str = issue["body"] or ""
        new_body, changes = fix_refs(body)
        if new_body == body:
            print(f"= #{num} unchanged")
            continue
        if apply:
            import tempfile
            path = tempfile.mktemp(suffix=".md", prefix=f"issue-{num}-")
            with open(path, "w", encoding="utf-8") as f:
                f.write(new_body)
            subprocess.run(
                ["gh", "issue", "edit", str(num), "--body-file", path],
                check=True,
            )
            print(f"+ #{num} edited ({len(changes)} replacements)")
        else:
            print(f"WOULD EDIT #{num} ({len(changes)} replacements):")
            for old, new in changes:
                print(f"    #{old} -> #{new}")
        changed.append(num)

    print(f"\n{'Applied' if apply else 'Would change'}: {changed}")


if __name__ == "__main__":
    main(apply="--apply" in sys.argv)
