from pathlib import Path
import re
import sys

import yaml


class UniqueKeyLoader(yaml.SafeLoader):
    pass


def unique_mapping(loader, node, deep=False):
    mapping = {}
    for key_node, value_node in node.value:
        key = loader.construct_object(key_node, deep=deep)
        if key in mapping:
            raise ValueError("Duplicate YAML key")
        mapping[key] = loader.construct_object(value_node, deep=deep)
    return mapping


UniqueKeyLoader.add_constructor(
    yaml.resolver.BaseResolver.DEFAULT_MAPPING_TAG,
    unique_mapping,
)

REPOSITORY = (
    "989200477899.dkr.ecr.us-east-2.amazonaws.com/"
    "dev-dealengine-ecr-repo"
)
DIGEST_PATTERN = r"sha256:[0-9a-f]{64}"


def reject():
    print("Invalid GitOps values or image digest.", file=sys.stderr)
    sys.exit(1)


if len(sys.argv) != 3:
    reject()

path = Path(sys.argv[1])
candidate = sys.argv[2]

if re.fullmatch(DIGEST_PATTERN, candidate) is None:
    reject()

try:
    original = path.read_bytes()
    text = original.decode("utf-8")
    document = yaml.load(text, Loader=UniqueKeyLoader)

    if not isinstance(document, dict):
        reject()

    image = document.get("image")
    if not isinstance(image, dict):
        reject()
    if image.get("repository") != REPOSITORY:
        reject()

    previous = image.get("digest")
    if not isinstance(previous, str):
        reject()
    if re.fullmatch(DIGEST_PATTERN, previous) is None:
        reject()

    lines = text.splitlines(keepends=True)
    starts = [
        index for index, line in enumerate(lines)
        if line.rstrip("\r\n") == "image:"
    ]
    if len(starts) != 1:
        reject()

    start = starts[0] + 1
    end = len(lines)
    for index in range(start, len(lines)):
        line = lines[index]
        if line.strip() and not line.startswith((" ", "\t", "#")):
            end = index
            break

    matches = []
    pattern = re.compile(
        r"^  digest:[ \t]*(" + DIGEST_PATTERN + r")[ \t]*(?:#[^\r\n]*)?(?:\r?\n)?$"
    )
    for index in range(start, end):
        match = pattern.fullmatch(lines[index])
        if match:
            matches.append((index, match))

    if len(matches) != 1:
        reject()

    index, match = matches[0]
    if match.group(1) != previous:
        reject()

    if candidate == previous:
        print("Digest already selected.")
        sys.exit(0)

    line = lines[index]
    lines[index] = line[:match.start(1)] + candidate + line[match.end(1):]
    updated = "".join(lines)

    expected = dict(image)
    expected["digest"] = candidate
    parsed_updated = yaml.load(updated, Loader=UniqueKeyLoader)
    if parsed_updated != {**document, "image": expected}:
        reject()

except (OSError, UnicodeError, ValueError, TypeError, yaml.YAMLError):
    reject()

path.write_bytes(updated.encode("utf-8"))
print("Updated dev image digest.")