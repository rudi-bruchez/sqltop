#!/usr/bin/env bash
# Set the version this binary reports. Usage:
#
#   scripts/bump-version.sh 0.2.0
#
# The version lives in one place, a constant in internal/buildinfo, because a
# version that depends on the build command is wrong the first time someone
# builds it differently. The commit and the dirty flag come from the toolchain
# on their own, so this script has one line to change.
#
# It does not commit and it does not tag. Cutting a tag is a decision about
# whether the milestone works, which spec section 11 puts on a person rather
# than on a script.
set -euo pipefail

VERSION="${1:-}"
FILE="$(dirname "$0")/../internal/buildinfo/buildinfo.go"

if [ -z "$VERSION" ]; then
  echo "usage: $(basename "$0") <version>   e.g. 0.2.0" >&2
  echo "current: $(grep -oP 'const Version = "\K[^"]+' "$FILE")" >&2
  exit 1
fi

# Refused rather than accepted quietly: a tag that does not sort is a tag
# someone has to explain later, and the spec's versioning section assumes
# major.minor.patch.
if ! [[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  echo "not a version: $VERSION (want major.minor.patch, optionally -something)" >&2
  exit 1
fi

OLD=$(grep -oP 'const Version = "\K[^"]+' "$FILE")
if [ "$OLD" = "$VERSION" ]; then
  echo "already $VERSION"
  exit 0
fi

sed -i "s/const Version = \"$OLD\"/const Version = \"$VERSION\"/" "$FILE"
gofmt -l "$FILE" >/dev/null

echo "$OLD -> $VERSION"
echo
echo "Next, by hand rather than by script:"
echo "  - move the CHANGELOG's unreleased section under $VERSION"
echo "  - commit"
echo "  - git tag -a v$VERSION -m 'sqltop $VERSION', once the milestone actually works"
