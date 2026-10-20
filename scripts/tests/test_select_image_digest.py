import json
from pathlib import Path
import subprocess
import sys
import unittest

SCRIPT = Path(__file__).resolve().parents[1] / "select_image_digest.py"
REPOSITORY = "989200477899.dkr.ecr.us-east-2.amazonaws.com/dev-dealengine-ecr-repo"
DIGEST = "sha256:" + "a" * 64
OTHER = "sha256:" + "b" * 64


class DigestTests(unittest.TestCase):
    def invoke(self, entries):
        return subprocess.run(
            [sys.executable, str(SCRIPT), REPOSITORY],
            input=json.dumps(entries),
            capture_output=True,
            text=True,
        )

    def test_selects_exact_repository(self):
        result = self.invoke([
            f"example.invalid/another@{OTHER}",
            f"{REPOSITORY}@{DIGEST}",
        ])
        self.assertEqual(result.returncode, 0)
        self.assertEqual(result.stdout, DIGEST + "\n")
        self.assertEqual(result.stderr, "")

    def test_rejects_missing_malformed_or_conflicting_digest(self):
        for entries in (
            [],
            [f"example.invalid/another@{DIGEST}"],
            [f"{REPOSITORY}@sha256:invalid"],
            [f"{REPOSITORY}@{DIGEST}", f"{REPOSITORY}@{OTHER}"],
        ):
            with self.subTest(entries=entries):
                result = self.invoke(entries)
                self.assertEqual(result.returncode, 1)
                self.assertEqual(result.stdout, "")
                self.assertEqual(
                    result.stderr,
                    "Expected one valid digest for the exact repository.\n",
                )


if __name__ == "__main__":
    unittest.main()