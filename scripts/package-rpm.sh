#!/usr/bin/env bash
# Build the Frappium .rpm from a bench payload.
#
# This does NOT build the bench. Run scripts/build-bench-payload.sh first, in a
# container of the distribution you are targeting -- the payload links against
# that distribution's python and OpenSSL, so a Rocky 9 payload cannot be
# packaged for Fedora and vice versa.
#
#   scripts/build-bench-payload.sh --distro rockylinux:9 --apps erpnext
#   scripts/package-rpm.sh --payload dist/payload-rockylinux-9-x86_64.tar.gz
#
# rpmbuild must be available. On a non-RPM host run this inside the same
# container family, e.g.:
#   podman run --rm -v "$PWD":/src:z -w /src rockylinux:9 \
#     sh -c 'dnf -y install rpm-build tar && scripts/package-rpm.sh'
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
# shellcheck source=scripts/lib/stage-native-root.sh
. "$SCRIPT_DIR/lib/stage-native-root.sh"

PAYLOAD=""
VERSION=""
OUTPUT_DIR="$ROOT_DIR/dist"

while [ $# -gt 0 ]; do
  case "$1" in
    --payload) PAYLOAD="$2"; shift 2 ;;
    --version) VERSION="$2"; shift 2 ;;
    --output)  OUTPUT_DIR="$2"; shift 2 ;;
    -h|--help) sed -n '2,18p' "$0"; exit 0 ;;
    *) echo "unknown option '$1'" >&2; exit 2 ;;
  esac
done

if [ -z "$PAYLOAD" ]; then
  found=()
  for p in "$OUTPUT_DIR"/payload-*.tar.gz; do [ -f "$p" ] && found+=("$p"); done
  case ${#found[@]} in
    1) PAYLOAD="${found[0]}"; echo "==> using payload ${PAYLOAD}" ;;
    0) echo "FATAL: no payload found in $OUTPUT_DIR." >&2
       echo "       scripts/build-bench-payload.sh --distro rockylinux:9" >&2; exit 1 ;;
    *) echo "FATAL: several payloads in $OUTPUT_DIR; pass --payload:" >&2
       printf '       %s\n' "${found[@]}" >&2; exit 1 ;;
  esac
fi

META="${PAYLOAD%.tar.gz}.env"
[ -f "$META" ] || { echo "FATAL: payload metadata '$META' missing" >&2; exit 1; }
# shellcheck disable=SC1090
. "$META"

[ "$FAMILY" = "rhel" ] || {
  echo "FATAL: payload was built for '$FAMILY' ($DISTRO); .rpm needs an rhel-family payload" >&2
  exit 1; }

case "$ARCH" in
  x86_64|aarch64) RPM_ARCH="$ARCH" ;;
  *) echo "FATAL: unsupported arch '$ARCH'" >&2; exit 1 ;;
esac

VERSION="${VERSION:-${FRAPPE_VERSION}}"
[ -n "$VERSION" ] && [ "$VERSION" != "unknown" ] || VERSION="16.0.0"
# rpm forbids '-' in Version. It appears in prereleases such as 16.0.0-dev.
VERSION="${VERSION//-/.}"
DISTRO_TAG="$(echo "$DISTRO" | tr -d ':.')"

command -v rpmbuild >/dev/null 2>&1 || {
  echo "FATAL: rpmbuild not found. Install rpm-build, or run this inside a" >&2
  echo "       container of the target distribution (see the header)." >&2; exit 1; }

WORK="$(mktemp -d "${TMPDIR:-/tmp}/frappium-rpm.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

STAGING="$WORK/root"
echo "==> building frappium ${VERSION} (${RPM_ARCH}) for ${DISTRO}"
stage_native_root "$STAGING" rhel "$PAYLOAD"

mkdir -p "$WORK/rpmbuild"/{BUILD,BUILDROOT,RPMS,SOURCES,SPECS,SRPMS}

# Source0 is the fully staged filesystem. The spec untars it straight into the
# buildroot: rpmbuild compiles nothing, because the bench was already built at
# its final path inside a container of this distribution.
echo "==> packing staged root as Source0"
tar --numeric-owner --owner=0 --group=0 \
    -C "$STAGING" -czf "$WORK/rpmbuild/SOURCES/frappium-root-${VERSION}.tar.gz" .

cp "$ROOT_DIR/packaging/frappium.spec" "$WORK/rpmbuild/SPECS/"

# The bundled interpreter's library dependencies were resolved at payload-build
# time by asking rpm which packages own the .so files it loads. Passed as a
# single space-separated list -- rpm accepts several names on one Requires line,
# and a multi-line --define value does not expand reliably.
RUNTIME_REQUIRES="$(echo "${RUNTIME_DEPS:-}" | tr -s ' ')"

rpmbuild -bb "$WORK/rpmbuild/SPECS/frappium.spec" \
  --define "_topdir $WORK/rpmbuild" \
  --define "frappium_version $VERSION" \
  --define "frappium_arch $RPM_ARCH" \
  --define "python_xy $PYTHON_XY" \
  --define "runtime_requires $RUNTIME_REQUIRES" \
  --define "distro_tag $DISTRO_TAG" \
  --define "frappe_branch $FRAPPE_BRANCH"

mkdir -p "$OUTPUT_DIR"
find "$WORK/rpmbuild/RPMS" -name '*.rpm' -exec cp -v {} "$OUTPUT_DIR/" \;

echo "==> built:"
find "$OUTPUT_DIR" -name "frappium-${VERSION}-*.rpm" -newer "$META" -print0 2>/dev/null \
  | xargs -0 -r ls -lh
