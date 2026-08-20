#!/usr/bin/env bash
# Update brew/Formula/vybench.rb url + sha256 for a given git tag.
#
# Usage:
#   ./scripts/update-brew-sha256.sh v16.0.0
#   ./scripts/update-brew-sha256.sh            # uses newest v* tag
#
# Homebrew requires a pinned digest in the formula; this script is what the
# release pipeline (and a human cutting a release) uses to compute it.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
FORMULA="$ROOT/brew/Formula/vybench.rb"
REPO_SLUG="${GITHUB_REPOSITORY:-vyogotech/vybench}"

TAG="${1:-}"
if [ -z "$TAG" ]; then
  TAG="$(git -C "$ROOT" tag -l 'v*' --sort=-v:refname | head -1 || true)"
fi
if [ -z "$TAG" ]; then
  echo "error: no tag given and no v* tags found" >&2
  exit 1
fi
case "$TAG" in
  v*) ;;
  *) echo "error: tag must look like v16.0.0 (got '$TAG')" >&2; exit 1 ;;
esac

URL="https://github.com/${REPO_SLUG}/archive/refs/tags/${TAG}.tar.gz"
TMP="$(mktemp)"
trap 'rm -f "$TMP"' EXIT

echo "Downloading $URL ..."
HTTP_CODE="$(curl -sL -o "$TMP" -w '%{http_code}' "$URL")"
if [ "$HTTP_CODE" != "200" ]; then
  echo "error: download failed HTTP $HTTP_CODE" >&2
  exit 1
fi
# Refuse HTML/error bodies disguised as tarballs.
if ! gzip -t "$TMP" 2>/dev/null; then
  echo "error: downloaded file is not a gzip tarball (tag missing?)" >&2
  exit 1
fi

SUM="$(shasum -a 256 "$TMP" | awk '{print $1}')"
SIZE="$(wc -c <"$TMP" | tr -d ' ')"
echo "sha256=$SUM size=$SIZE"

if [ ! -f "$FORMULA" ]; then
  echo "error: formula not found at $FORMULA" >&2
  exit 1
fi

# Rewrite url + sha256 lines for the stable bottle source.
python3 - "$FORMULA" "$URL" "$SUM" <<'PY'
import pathlib, re, sys
path, url, digest = pathlib.Path(sys.argv[1]), sys.argv[2], sys.argv[3]
text = path.read_text()
text, n_url = re.subn(
    r'(?m)^(\s*)url\s+"https://github\.com/[^"]+/archive/refs/tags/v[^"]+\.tar\.gz"',
    rf'\1url "{url}"',
    text,
    count=1,
)
text, n_sha = re.subn(
    r'(?m)^(\s*)sha256\s+"(?:[0-9a-fA-F]{64}|REPLACE_WITH_ACTUAL_SHA256_AFTER_TAGGING)"',
    rf'\1sha256 "{digest}"',
    text,
    count=1,
)
if n_url != 1 or n_sha != 1:
    raise SystemExit(f"error: expected to rewrite 1 url and 1 sha256 (url={n_url}, sha={n_sha})")
path.write_text(text)
print(f"updated {path}")
PY

echo "Done. Diff:"
git -C "$ROOT" --no-pager diff -- "$FORMULA" || true
