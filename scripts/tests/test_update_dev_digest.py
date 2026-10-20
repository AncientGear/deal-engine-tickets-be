from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

SCRIPT = Path(__file__).resolve().parents[1] / "update_dev_digest.py"
REPOSITORY = "989200477899.dkr.ecr.us-east-2.amazonaws.com/dev-dealengine-ecr-repo"
OLD = "sha256:" + "a" * 64
NEW = "sha256:" + "b" * 64

VALUES = (
    "# Preserve this comment\n"
    "replicaCount: 1\n"
    "\n"
    "image:\n"
    f"  repository: {REPOSITORY}\n"
    f"  digest: {OLD}\n"
    "\n"
    "existingSecret: backend-runtime\n"
    "rdsCa:\n"
    "  enabled: true\n"
)


class UpdateDigestTests(unittest.TestCase):
    def invoke(self, original, digest=NEW):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "values.yaml"
            path.write_bytes(original.encode())
            result = subprocess.run(
                [sys.executable, str(SCRIPT), str(path), digest],
                capture_output=True, text=True,
            )
            return result, path.read_bytes().decode()

    def test_changes_only_the_digest(self):
        result, updated = self.invoke(VALUES)
        self.assertEqual(result.returncode, 0)
        self.assertEqual(result.stdout, "Updated dev image digest.\n")
        self.assertEqual(result.stderr, "")
        self.assertEqual(updated, VALUES.replace(OLD, NEW))

    def test_identical_digest_is_a_noop(self):
        result, updated = self.invoke(VALUES, OLD)
        self.assertEqual(result.returncode, 0)
        self.assertEqual(result.stdout, "Digest already selected.\n")
        self.assertEqual(result.stderr, "")
        self.assertEqual(updated, VALUES)

    def test_rejected_input_leaves_file_unchanged(self):
        cases = (
            (VALUES, "sha256:invalid"),
            (VALUES.replace(REPOSITORY, "example.invalid/other"), NEW),
            (VALUES.replace(f"  digest: {OLD}\n", ""), NEW),
            (VALUES.replace(
                f"  digest: {OLD}\n",
                f"  digest: {OLD}\n  digest: {NEW}\n",
            ), NEW),
        )
        for original, digest in cases:
            with self.subTest(original=original, digest=digest):
                result, updated = self.invoke(original, digest)
                self.assertEqual(result.returncode, 1)
                self.assertEqual(result.stdout, "")
                self.assertEqual(
                    result.stderr,
                    "Invalid GitOps values or image digest.\n",
                )
                self.assertEqual(updated, original)


if __name__ == "__main__":
    unittest.main()