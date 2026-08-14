#!/usr/bin/env bash
# Detect drift between the vendored nginx template and its upstream original.
#
# WHY THIS EXISTS
#
# The nginx vhost has one canonical source: the container images'
# upload/src/nginx/frappe.conf.template. The packaging code deliberately reuses
# it rather than maintaining a second copy, because the two must agree about how
# /assets, /files, /socket.io and the X-Accel-Redirect handoff are routed.
#
# While packaging lives in the same repository as the images, that reuse is a
# path. Once packaging moves to its own repository the path disappears, the copy
# silently freezes, and a routing fix made for the images never reaches the
# packages -- the exact failure that produced the snap's third divergent copy.
#
# So the packaging repo vendors the template and this check fails the build when
# the vendored copy no longer matches upstream. Vendoring keeps builds hermetic;
# the check keeps them honest.
#
# Usage:
#   scripts/check-nginx-template-drift.sh                 # compare against $UPSTREAM_URL
#   scripts/check-nginx-template-drift.sh --update        # accept upstream as the new baseline
#   scripts/check-nginx-template-drift.sh --local PATH    # compare against a checkout
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

# The copy under test. In the images repository the canonical file is present
# under upload/ and the check is trivially satisfied; in the standalone packaging
# repository only the vendored copy exists. Look in both, same as
# scripts/lib/stage-native-root.sh, so this script works unchanged in either.
if [ -z "${VENDORED:-}" ]; then
  if [ -f "$ROOT_DIR/upload/src/nginx/frappe.conf.template" ]; then
    VENDORED="$ROOT_DIR/upload/src/nginx/frappe.conf.template"
  else
    VENDORED="$ROOT_DIR/vendor/nginx/frappe.conf.template"
  fi
fi

# Where the canonical version lives. Override for a private repository.
UPSTREAM_URL="${UPSTREAM_URL:-https://raw.githubusercontent.com/vyogotech/frappista/version-16/upload/src/nginx/frappe.conf.template}"

MODE=compare
LOCAL_PATH=""
while [ $# -gt 0 ]; do
  case "$1" in
    --update) MODE=update; shift ;;
    --local)  LOCAL_PATH="$2"; shift 2 ;;
    -h|--help) sed -n '2,24p' "$0"; exit 0 ;;
    *) echo "unknown option '$1'" >&2; exit 2 ;;
  esac
done

[ -f "$VENDORED" ] || { echo "FATAL: vendored template not found at $VENDORED" >&2; exit 1; }

TMP="$(mktemp -d "${TMPDIR:-/tmp}/nginx-drift.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
UPSTREAM="$TMP/upstream.template"

if [ -n "$LOCAL_PATH" ]; then
  [ -f "$LOCAL_PATH" ] || { echo "FATAL: --local '$LOCAL_PATH' not found" >&2; exit 1; }
  cp "$LOCAL_PATH" "$UPSTREAM"
  SOURCE="$LOCAL_PATH"
else
  SOURCE="$UPSTREAM_URL"
  # A private repository needs a token. Fail with something actionable rather
  # than silently comparing against a 404 body.
  AUTH=()
  [ -n "${GITHUB_TOKEN:-}" ] && AUTH=(-H "Authorization: Bearer $GITHUB_TOKEN")
  if ! curl -fsSL "${AUTH[@]}" "$UPSTREAM_URL" -o "$UPSTREAM"; then
    echo "FATAL: could not fetch $UPSTREAM_URL" >&2
    echo "       For a private source repository, set GITHUB_TOKEN, or compare" >&2
    echo "       against a local checkout: --local /path/to/frappe.conf.template" >&2
    exit 1
  fi
fi

if [ "$MODE" = "update" ]; then
  cp "$UPSTREAM" "$VENDORED"
  echo "==> vendored template updated from $SOURCE"
  echo "    Review the diff before committing -- the packaging copy differs from"
  echo "    the container one only in \$BENCH_SITES and \$NGINX_PORT, and those"
  echo "    substitutions are applied at package time, not stored here."
  exit 0
fi

if diff -u "$VENDORED" "$UPSTREAM" > "$TMP/diff"; then
  echo "==> nginx template is in sync with $SOURCE"
  exit 0
fi

cat >&2 <<EOF
FATAL: the vendored nginx template has drifted from upstream.

  vendored: $VENDORED
  upstream: $SOURCE

$(cat "$TMP/diff")

If the upstream change is one the packages should adopt:
    scripts/check-nginx-template-drift.sh --update

If the packages must diverge deliberately, record why in the file header and
update UPSTREAM_URL (or pin it to a tag) so this check stops comparing against a
moving target.
EOF
exit 1
