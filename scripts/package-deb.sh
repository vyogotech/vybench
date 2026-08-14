#!/usr/bin/env bash
# Build the Frappium .deb from a bench payload.
#
# This does NOT build the bench. Run scripts/build-bench-payload.sh first, in a
# container of the distribution you are targeting -- the payload links against
# that distribution's python and OpenSSL, so an Ubuntu 24.04 payload cannot be
# packaged for Debian 12 and vice versa.
#
#   scripts/build-bench-payload.sh --distro ubuntu:24.04 --apps erpnext
#   scripts/package-deb.sh --payload dist/payload-ubuntu-24.04-x86_64.tar.gz
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
# shellcheck source=scripts/lib/stage-native-root.sh
. "$SCRIPT_DIR/lib/stage-native-root.sh"

PAYLOAD=""
VERSION=""
OUTPUT_DIR="$ROOT_DIR/dist"
STAGING=""

while [ $# -gt 0 ]; do
  case "$1" in
    --payload) PAYLOAD="$2"; shift 2 ;;
    --version) VERSION="$2"; shift 2 ;;
    --output)  OUTPUT_DIR="$2"; shift 2 ;;
    -h|--help) sed -n '2,12p' "$0"; exit 0 ;;
    *) echo "unknown option '$1'" >&2; exit 2 ;;
  esac
done

# Sole payload in dist/ is the common case; pick it up rather than making the
# caller retype a long generated filename.
if [ -z "$PAYLOAD" ]; then
  # A plain glob rather than mapfile: bash 3.2 is still what ships on macOS, and
  # a maintainer staging a tree locally should not hit a syntax error.
  found=()
  for p in "$OUTPUT_DIR"/payload-*.tar.gz; do [ -f "$p" ] && found+=("$p"); done
  case ${#found[@]} in
    1) PAYLOAD="${found[0]}"; echo "==> using payload ${PAYLOAD}" ;;
    0) echo "FATAL: no payload found in $OUTPUT_DIR." >&2
       echo "       scripts/build-bench-payload.sh --distro ubuntu:24.04" >&2; exit 1 ;;
    *) echo "FATAL: several payloads in $OUTPUT_DIR; pass --payload:" >&2
       printf '       %s\n' "${found[@]}" >&2; exit 1 ;;
  esac
fi

META="${PAYLOAD%.tar.gz}.env"
[ -f "$META" ] || { echo "FATAL: payload metadata '$META' missing" >&2; exit 1; }
# shellcheck disable=SC1090
. "$META"

[ "$FAMILY" = "debian" ] || {
  echo "FATAL: payload was built for '$FAMILY' ($DISTRO); .deb needs a debian-family payload" >&2
  exit 1; }

case "$ARCH" in
  x86_64)  DEB_ARCH=amd64 ;;
  aarch64) DEB_ARCH=arm64 ;;
  *) echo "FATAL: unsupported arch '$ARCH'" >&2; exit 1 ;;
esac

VERSION="${VERSION:-${FRAPPE_VERSION}}"
[ -n "$VERSION" ] && [ "$VERSION" != "unknown" ] || VERSION="16.0.0"
# A '-' in a Debian version separates upstream from the packaging revision, so
# "16.0.0-dev" would parse as upstream 16.0.0, revision "dev". Fold it into the
# upstream part and use a real revision.
UPSTREAM="${VERSION//-/.}"
# The distro goes in the revision as a '~' suffix, the conventional marker for a
# distribution-specific rebuild: it sorts BEFORE the plain revision, and it makes
# `dpkg -l` show which build is installed. Necessary here rather than cosmetic --
# the payload is tied to one distro release's python.
DISTRO_TAG="$(echo "$DISTRO" | tr -d ':.' )"
FULL_VERSION="${UPSTREAM}-1~${DISTRO_TAG}"

STAGING="$(mktemp -d "${TMPDIR:-/tmp}/frappium-deb.XXXXXX")"
trap 'rm -rf "$STAGING"' EXIT

echo "==> building frappium ${FULL_VERSION} (${DEB_ARCH}) for ${DISTRO}"
stage_native_root "$STAGING" debian "$PAYLOAD"

mkdir -p "$STAGING/DEBIAN" "$OUTPUT_DIR"

# No python3 dependency: frappe v16 requires exactly 3.14, which no Debian or
# Ubuntu release ships, so the interpreter is bundled at /usr/lib/frappium/python
# and the package does not use the system python at all.
#
# It does link against this release's OpenSSL, libffi, sqlite and so on. Those
# package names are resolved at build time by asking dpkg which packages own the
# libraries the interpreter actually loads -- hardcoding them would break on
# Ubuntu 24.04's t64 renames (libssl3t64, libreadline8t64) and produce an
# uninstallable package.
DEP_LINES=""
for dep in ${RUNTIME_DEPS:-}; do
  DEP_LINES="${DEP_LINES} ${dep},
"
done

INSTALLED_SIZE="$(du -sk "$STAGING" | cut -f1)"

cat > "$STAGING/DEBIAN/control" <<EOF
Package: frappium
Version: ${FULL_VERSION}
Architecture: ${DEB_ARCH}
Maintainer: Vyogo Labs <dev@vyogolabs.tech>
Installed-Size: ${INSTALLED_SIZE}
Section: admin
Priority: optional
Homepage: https://github.com/vyogotech/frappium
Replaces: frappista, frappista-bench, frappium-bench
Conflicts: frappista, frappista-bench, frappium-bench
Depends: mariadb-server (>= 10.6),
 mariadb-client,
 redis-server (>= 6.0),
 nginx,
 libmariadb3,
 gettext-base,
 util-linux,
 git,
 curl,
${DEP_LINES} ca-certificates
Recommends: wkhtmltopdf, fontconfig, libxrender1, libxext6
Description: Frappe Framework and ERPNext, packaged for Debian and Ubuntu
 A complete single-node Frappe/ERPNext deployment: the bench (apps, virtualenv
 and built assets) plus systemd units for the web server, the three background
 worker queues, the scheduler and the realtime server.
 .
 Uses the distribution's own MariaDB, Redis and nginx rather than bundling them.
 Python ${PYTHON_VERSION} is bundled, because frappe v16 requires exactly 3.14
 and no Debian or Ubuntu release ships it.
 Built for ${DISTRO}; frappe ${FRAPPE_VERSION}.
 .
 Run 'frappium-setup' after installing to create the database account, the
 first site and the nginx vhost.
EOF

cat > "$STAGING/DEBIAN/conffiles" <<'EOF'
/etc/frappium/frappium.env
/etc/mysql/mariadb.conf.d/10-frappe.cnf
EOF

cat > "$STAGING/DEBIAN/postinst" <<'EOF'
#!/bin/sh
set -e

BENCH=/opt/frappe-bench

if [ "$1" = "configure" ]; then
  # A real system account, not a placeholder: bench refuses to run as root, and
  # frappe's change_uid() calls getpwnam() on the configured frappe_user -- which
  # raises KeyError and kills the process if that user does not exist.
  # Home is the bench itself, so nothing scatters into /home.
  # useradd/groupadd from the `passwd` package (priority: required), not adduser
  # (priority: important) -- adduser is absent from minimal images and container
  # base images, where postinst then dies with "adduser: not found" and leaves
  # the package half-configured. This also keeps the account identical to the
  # one the .rpm's %pre creates.
  getent group frappe >/dev/null 2>&1 || groupadd --system frappe
  getent passwd frappe >/dev/null 2>&1 || \
    useradd --system --gid frappe --home-dir "$BENCH" --no-create-home \
            --shell /usr/sbin/nologin \
            --comment "Frappe application account" frappe

  # is_bench_directory() needs all five; config/pids is the one that goes
  # missing and its absence makes `bench worker` fail with "No such command".
  install -d -o frappe -g frappe -m 2770 \
    "$BENCH/apps" "$BENCH/sites" "$BENCH/config" "$BENCH/config/pids" "$BENCH/logs"

  # The payload ships with numeric owner 0:0 because the frappe uid is not known
  # at build time. Claim it now. Group-shared, never world-writable -- the same
  # model the container image uses (chown 1001:0 + chmod ug+rwX).
  chown -R frappe:frappe "$BENCH"
  chmod -R ug+rwX,o-rwx "$BENCH"
  # Nothing here is world-readable: sites/*/private holds uploaded documents and
  # site_config.json holds the database password. nginx gets access by being put
  # in the frappe group (frappium-setup does that), not by opening the tree up.

  # Everyone expects `bench`. Only claim the name if it is free -- a
  # pip-installed frappe-bench in /usr/local/bin is a real and common setup, and
  # silently replacing it would be worse than not having the shortcut.
  if [ ! -e /usr/local/bin/bench ]; then
    ln -sf "$BENCH/env/bin/bench" /usr/local/bin/bench
  fi

  systemctl daemon-reload >/dev/null 2>&1 || true

  cat <<'MSG'

Frappium is installed but not yet configured.

  sudo frappium-setup --site dev.localhost --admin-password admin

That creates the database account, the site, the nginx vhost, and starts the
services. Nothing is started before you run it.
MSG
fi

exit 0
EOF

cat > "$STAGING/DEBIAN/prerm" <<'EOF'
#!/bin/sh
set -e

if [ "$1" = "remove" ] || [ "$1" = "deconfigure" ]; then
  # Stop the app tier but leave mariadb, redis and nginx alone -- this package
  # did not start them exclusively for itself and other things may use them.
  systemctl stop 'frappe-worker@*.service' >/dev/null 2>&1 || true
  systemctl stop frappe-web.service frappe-scheduler.service \
                 frappe-socketio.service frappium.target >/dev/null 2>&1 || true
  systemctl disable 'frappe-worker@*.service' >/dev/null 2>&1 || true
  systemctl disable frappe-web.service frappe-scheduler.service \
                    frappe-socketio.service frappium.target >/dev/null 2>&1 || true
fi

exit 0
EOF

cat > "$STAGING/DEBIAN/postrm" <<'EOF'
#!/bin/sh
set -e

case "$1" in
  remove|purge)
    if [ -L /usr/local/bin/bench ] && \
       [ "$(readlink /usr/local/bin/bench)" = "/opt/frappe-bench/env/bin/bench" ]; then
      rm -f /usr/local/bin/bench
    fi

    # frappium-setup wrote this; dpkg does not know about it.
    if [ -f /etc/nginx/conf.d/frappe.conf ]; then
      rm -f /etc/nginx/conf.d/frappe.conf
      # Put back whatever we retired to make room for it.
      if [ -e /etc/nginx/sites-available/default.frappium-disabled ] && \
         [ ! -e /etc/nginx/sites-enabled/default ]; then
        mv /etc/nginx/sites-available/default.frappium-disabled \
           /etc/nginx/sites-enabled/default
      fi
      systemctl reload nginx >/dev/null 2>&1 || true
    fi

    systemctl daemon-reload >/dev/null 2>&1 || true
    ;;
esac

# Sites, uploaded files and databases are NOT removed, on remove or on purge.
# They are not package files, they are the user's data, and dpkg has no way to
# tell the difference between "uninstalling" and "about to reinstall".
if [ "$1" = "purge" ] && [ -d /opt/frappe-bench/sites ]; then
  cat <<'MSG'
Site data is still in /opt/frappe-bench/sites, and the databases are still in
MariaDB. Remove them by hand if you meant to.
MSG
fi

exit 0
EOF

chmod 755 "$STAGING/DEBIAN/postinst" "$STAGING/DEBIAN/prerm" "$STAGING/DEBIAN/postrm"

command -v dpkg-deb >/dev/null 2>&1 || {
  echo "FATAL: dpkg-deb not found. Build the .deb on a Debian-family host or in" >&2
  echo "       a container: staged tree is at $STAGING (not cleaned up)." >&2
  trap - EXIT; exit 1; }

mkdir -p "$OUTPUT_DIR"
PKG="$OUTPUT_DIR/frappium_${FULL_VERSION}_${DEB_ARCH}.deb"
echo "==> dpkg-deb --build"
# --root-owner-group forces 0:0 ownership regardless of who runs the build;
# without it a non-root build bakes in the builder's uid.
dpkg-deb --root-owner-group -Zgzip --build "$STAGING" "$PKG"

echo "==> $PKG"
ls -lh "$PKG"
dpkg-deb --info "$PKG" | sed 's/^/    /'
