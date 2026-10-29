"""Offline workflow checks; requires the existing CI dependency PyYAML."""
import json
import subprocess
import sys
import textwrap
import unittest
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parent
PUBLISH = yaml.safe_load((ROOT / "publish-floci.yml").read_text())
CALLER = yaml.safe_load((ROOT / "local-delivery.yml").read_text())
STEPS = PUBLISH["jobs"]["publish"]["steps"]


def inline_python(step):
    return step["run"].split("python3 -c '\n", 1)[1].split("\n')", 1)[0]


def invoke(code, value):
    # YAML block indentation remains inside the embedded Python program.
    return subprocess.run(
        [sys.executable, "-c", textwrap.dedent(code)],
        input=value, text=True, capture_output=True,
    )


class LocalDeliveryTests(unittest.TestCase):
    def test_shell_syntax(self):
        for step in STEPS:
            if "run" in step:
                result = subprocess.run(
                    ["bash", "-n"], input=step["run"],
                    text=True, capture_output=True,
                )
                self.assertEqual(result.returncode, 0, result.stderr)

    def test_source_run_gate(self):
        code = inline_python(STEPS[0])
        run = {"path": ".github/workflows/ci.yml", "event": "push",
               "head_branch": "dev", "status": "completed",
               "conclusion": "success", "head_sha": "a" * 40,
               "run_attempt": 2}
        result = invoke(code, json.dumps(run))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "backend-image-" + "a" * 40 + "-2\n")
        for field, value in (
            ("path", ".github/workflows/other.yml"),
            ("event", "pull_request"), ("head_branch", "prod"),
            ("status", "in_progress"), ("conclusion", "failure"),
        ):
            with self.subTest(field=field):
                invalid = dict(run, **{field: value})
                self.assertNotEqual(
                    invoke(code, json.dumps(invalid)).returncode, 0)

    def test_exact_push_digest(self):
        code = inline_python(STEPS[2])
        digest = "sha256:" + "a" * 64
        result = invoke(code, "tag: digest: " + digest + " size: 123\n")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, digest + "\n")
        for value in ("", "digest: sha256:bad", "digest: " + digest + "f",
                      "digest: " + digest + "\ndigest: " + digest):
            self.assertNotEqual(invoke(code, value).returncode, 0)

    def test_publication_contract(self):
        job = PUBLISH["jobs"]["publish"]
        self.assertIn("self-hosted", job["runs-on"])
        self.assertIn("local-delivery", job["runs-on"])
        self.assertIn("workflow_dispatch", job["if"])
        self.assertIn("refs/heads/dev", job["if"])
        self.assertEqual(STEPS[1]["with"]["run-id"],
                         "${{ inputs.source-run-id }}")
        script = STEPS[2]["run"]
        self.assertIn('--endpoint-url "$FLOCI_ENDPOINT_URL"', script)
        self.assertIn('[[ "$mutability" == IMMUTABLE ]]', script)
        self.assertIn('[[ "$recorded" == "$digest" ]]', script)
        self.assertIn('[[ "$platform" == linux/arm64 ]]', script)
        self.assertNotIn("docker build", script)
        self.assertEqual(CALLER["jobs"]["publish"]["uses"],
                         "./.github/workflows/publish-floci.yml")


if __name__ == "__main__":
    unittest.main()
