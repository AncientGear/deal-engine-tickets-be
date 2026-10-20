import json
import re
import sys


def reject():
    print(
        "Expected one valid digest for the exact repository.",
        file=sys.stderr,
    )
    sys.exit(1)


if len(sys.argv) != 2 or not sys.argv[1]:
    reject()

repository = sys.argv[1]
prefix = repository + "@"

try:
    entries = json.load(sys.stdin)
except (ValueError, UnicodeError):
    reject()

if not isinstance(entries, list):
    reject()

digests = set()

for entry in entries:
    if not isinstance(entry, str):
        reject()

    if not entry.startswith(prefix):
        continue

    digest = entry[len(prefix):]
    if re.fullmatch(r"sha256:[0-9a-f]{64}", digest) is None:
        reject()

    digests.add(digest)

if len(digests) != 1:
    reject()

print(next(iter(digests)))