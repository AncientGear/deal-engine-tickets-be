import base64
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

import yaml

ROOT = Path(__file__).resolve().parents[2]
WORKFLOW = ROOT / ".github/workflows/ci.yml"
DIGEST = "sha256:" + "b" * 64
VALUES = f"image:\n  digest: {DIGEST}\n"

MOCK = """#!/usr/bin/env python3
import json
import os
from pathlib import Path
import sys

args = sys.argv[1:]
if Path(sys.argv[0]).name == "git":
    command = args[2:]
    if command == ["diff", "--quiet"]:
        sys.exit(0 if os.environ["MODE"] == "noop" else 1)
    if command == ["diff", "--name-only"]:
        print("unexpected.yaml" if os.environ["MODE"] == "unexpected"
                else "environments/dev/values.yaml")
    elif command == ["rev-parse", "HEAD"]:
        print("a" * 40)
    elif command == ["rev-parse", "HEAD:environments/dev/values.yaml"]:
        print("c" * 40)
    else:
        sys.exit(90)
else:
    log = Path(os.environ["CALLS"])
    previous = log.read_text().splitlines() if log.exists() else []
    with log.open("a") as output:
        output.write(json.dumps(args) + "\\n")
    if len(previous) + 1 == int(os.environ["FAIL_CALL"]):
        print("Simulated GitHub API failure.", file=sys.stderr)
        sys.exit(29)
    if args[1].endswith("/pulls"):
        print("https://github.com/AncientGear/dealengine-argo-ops/pull/999")
"""


def field(arguments, name):
    return next(
        item.split("=", 1)[1]
        for item in arguments if item.startswith(name + "=")
    )


class ProposalTests(unittest.TestCase):
    def invoke(self, mode="change", fail_call=0):
        document = yaml.load(WORKFLOW.read_text(), Loader=yaml.BaseLoader)
        step = next(
            step for step in document["jobs"]["propose-dev"]["steps"]
            if step.get("name") == "Propose dev deployment"
        )

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary = root / "bin"
            binary.mkdir()
            for name in ("git", "gh"):
                executable = binary / name
                executable.write_text(MOCK)
                executable.chmod(0o755)

            values = root / "gitops/environments/dev/values.yaml"
            values.parent.mkdir(parents=True)
            values.write_text(VALUES)
            calls = root / "calls.jsonl"
            summary = root / "summary"
            env = {
                **os.environ,
                "PATH": f"{binary}:{os.environ['PATH']}",
                "MODE": mode,
                "FAIL_CALL": str(fail_call),
                "CALLS": str(calls),
                "GH_TOKEN": "fake-test-token",
                "IMAGE_DIGEST": DIGEST,
                "GITHUB_RUN_ID": "123",
                "GITHUB_RUN_ATTEMPT": "2",
                "GITHUB_SHA": "d" * 40,
                "GITHUB_SERVER_URL": "https://github.com",
                "GITHUB_REPOSITORY": "AncientGear/deal-engine-tickets-be",
                "GITHUB_STEP_SUMMARY": str(summary),
            }
            result = subprocess.run(
                ["bash", "-e", "-o", "pipefail", "-c", step["run"]],
                cwd=root, env=env, capture_output=True, text=True,
            )
            recorded = [
                json.loads(line) for line in calls.read_text().splitlines()
            ] if calls.exists() else []
            return result, recorded, summary.read_text() if summary.exists() else ""

    def test_creates_only_branch_file_commit_and_pr(self):
        result, calls, summary = self.invoke()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "")
        self.assertEqual(result.stderr, "")
        self.assertEqual(len(calls), 3)

        prefix = "repos/AncientGear/dealengine-argo-ops"
        self.assertEqual(calls[0][1], prefix + "/git/refs")
        self.assertEqual(field(calls[0], "ref"), "refs/heads/automation/dev-image/123-2")
        self.assertEqual(field(calls[0], "sha"), "a" * 40)

        self.assertEqual(calls[1][1], prefix + "/contents/environments/dev/values.yaml")
        self.assertEqual(field(calls[1], "branch"), "automation/dev-image/123-2")
        self.assertEqual(field(calls[1], "sha"), "c" * 40)
        self.assertEqual(base64.b64decode(field(calls[1], "content")).decode(), VALUES)

        self.assertEqual(calls[2][1], prefix + "/pulls")
        self.assertEqual(field(calls[2], "base"), "main")
        self.assertEqual(field(calls[2], "head"), "automation/dev-image/123-2")
        self.assertIn(DIGEST, field(calls[2], "body"))
        self.assertEqual(
            summary,
            "GitOps proposal: https://github.com/AncientGear/dealengine-argo-ops/pull/999\n",
        )

    def test_noop_does_not_call_github(self):
        result, calls, summary = self.invoke(mode="noop")
        self.assertEqual(result.returncode, 0)
        self.assertEqual(result.stdout, "Digest already selected; no PR required.\n")
        self.assertEqual(result.stderr, "")
        self.assertEqual(calls, [])
        self.assertEqual(summary, "")

    def test_unexpected_change_does_not_call_github(self):
        result, calls, summary = self.invoke(mode="unexpected")
        self.assertEqual(result.returncode, 1)
        self.assertEqual(result.stdout, "")
        self.assertEqual(result.stderr, "Unexpected GitOps changes; proposal aborted.\n")
        self.assertEqual(calls, [])
        self.assertEqual(summary, "")

    def test_api_failure_stops_without_success_summary(self):
        for failure in (1, 2, 3):
            with self.subTest(failure=failure):
                result, calls, summary = self.invoke(fail_call=failure)
                self.assertEqual(result.returncode, 29)
                self.assertEqual(result.stdout, "")
                self.assertEqual(result.stderr, "Simulated GitHub API failure.\n")
                self.assertEqual(len(calls), failure)
                self.assertEqual(summary, "")


if __name__ == "__main__":
    unittest.main()