#!/usr/bin/env bash
# Build the Frappe bench payload that the .deb and .rpm ship.
#
# The bench is built INSIDE a container of the target distribution, at
# /opt/frappe-bench -- the exact path the package installs to. That is the whole
# trick: venv shebangs, pyvenv.cfg's `home`, and the sites/assets symlinks are
# all absolute, and building at the final path makes every one of them correct
# by construction instead of by post-processing.
#
# Do NOT build somewhere else and relocate. Getting that wrong is the defect
# class that cost the snap two debugging cycles (docs/native-packaging-plan.md,
# defects 8 and 9), and it is invisible on the build host because the build
# paths still exist there.
#
# The unavoidable consequence: even with Python bundled (see PYTHON_HOME below),
# that interpreter is compiled against the distribution's OpenSSL, libffi and
# sqlite, so the payload is specific to one distro release. One artefact per row
# of the build matrix, unlike the snap's single universal artefact.
#
# Usage:
#   scripts/build-bench-payload.sh [--distro ubuntu:24.04] [--frappe-branch version-16]
#                                  [--apps erpnext] [--output ./dist] [--engine podman]
set -euo pipefail

NODE_VERSION="24.19.0"
BENCH_CLI_VERSION="5.31.0"
BENCH_DIR=/opt/frappe-bench

# Frappe v16 pins `requires-python = ">=3.14,<3.15"`. No distribution in the
# support matrix ships 3.14 -- Ubuntu 24.04 has 3.12, Debian 12 has 3.11, RHEL 9
# has 3.9/3.11 -- so the interpreter is built from source and shipped with the
# package. (v15 allowed >=3.10 and could have used the system python; v16 cannot.)
#
# It is NOT installed under $BENCH_DIR, because `bench init` refuses a non-empty
# target directory and the interpreter must already exist at its final path when
# the venv is created -- that path is what pyvenv.cfg records. A private
# directory under /usr/lib is the FHS-correct home for a package-owned runtime.
PYTHON_VERSION="3.14.7"
PYTHON_HOME=/usr/lib/vybench/python

# ---------------------------------------------------------------------------
# Inner build -- runs inside the target-distribution container
# ---------------------------------------------------------------------------
if [ "${VYBENCH_BUILD_INNER:-0}" = "1" ]; then
  DISTRO="${DISTRO:?}"
  FRAPPE_BRANCH="${FRAPPE_BRANCH:?}"
  EXTRA_APPS="${EXTRA_APPS:-}"
  PYTHON_BIN="${PYTHON_BIN:-}"

  echo "==> inner build: $DISTRO, frappe $FRAPPE_BRANCH, apps: ${EXTRA_APPS:-none}"

  case "$DISTRO" in
    ubuntu:*|debian:*)
      FAMILY=debian
      export DEBIAN_FRONTEND=noninteractive
      apt-get update -qq
      # The -dev packages here are what decide which stdlib modules CPython ends
      # up with. A missing one does not fail the build: configure just skips that
      # module, and you find out later when `import ssl` fails at runtime. The
      # assertions after `make install` exist for exactly that reason.
      apt-get install -y --no-install-recommends \
        ca-certificates curl git xz-utils \
        gcc g++ make pkg-config python3 \
        libmariadb-dev libjpeg-dev libxml2-dev libxslt1-dev \
        libssl-dev libffi-dev zlib1g-dev libbz2-dev liblzma-dev \
        libsqlite3-dev libreadline-dev libncurses-dev libgdbm-dev uuid-dev \
        locales
      ;;
    rockylinux:*|almalinux:*|rhel:*|centos:*|fedora:*)
      FAMILY=rhel
      dnf -y install dnf-plugins-core >/dev/null 2>&1 || true
      dnf -y install epel-release >/dev/null 2>&1 || true
      # RHEL 9 keeps several -devel packages (gdbm-devel among them) in CRB,
      # which ships disabled. Without it the build dies on "No match for
      # argument: gdbm-devel". The repo is called crb on 9, powertools on 8, and
      # neither exists on Fedora -- try each and move on.
      dnf config-manager --set-enabled crb >/dev/null 2>&1 \
        || dnf config-manager --set-enabled powertools >/dev/null 2>&1 || true

      # `curl` is deliberately absent from this list: the RHEL base images ship
      # curl-minimal, which already provides /usr/bin/curl and *conflicts* with
      # the full curl package -- asking for curl fails the whole transaction.
      # Required: every one of these backs a stdlib module the assertions check.
      dnf -y install \
        ca-certificates git xz \
        gcc gcc-c++ make pkgconf-pkg-config redhat-rpm-config python3 \
        mariadb-connector-c-devel libjpeg-turbo-devel libxml2-devel libxslt-devel \
        openssl-devel libffi-devel zlib-devel bzip2-devel xz-devel \
        sqlite-devel readline-devel ncurses-devel

      # Only pull curl in if the image genuinely lacks it, and then allow the
      # swap that curl-minimal requires.
      command -v curl >/dev/null 2>&1 || dnf -y install --allowerasing curl

      # Optional: these only add accelerators (_gdbm, the C _uuid backend). Their
      # absence is survivable, and the stdlib assertions below are the real gate
      # -- so a missing repo must not fail the whole build.
      for opt in gdbm-devel libuuid-devel; do
        dnf -y install "$opt" >/dev/null 2>&1 || echo "NOTE: optional $opt unavailable; continuing"
      done
      ;;
    *)
      echo "FATAL: unsupported distro '$DISTRO'" >&2; exit 1 ;;
  esac

  # --- CPython, built at its final path ------------------------------------
  echo "==> building CPython $PYTHON_VERSION into $PYTHON_HOME"
  rm -rf "$PYTHON_HOME" /tmp/pysrc
  mkdir -p /tmp/pysrc "$(dirname "$PYTHON_HOME")"
  curl -fsSL "https://www.python.org/ftp/python/${PYTHON_VERSION}/Python-${PYTHON_VERSION}.tgz" \
    | tar -xzf - --strip-components=1 -C /tmp/pysrc
  (
    cd /tmp/pysrc
    # Deliberately NOT --enable-shared: without it libpython is linked into the
    # interpreter binary, so it needs no LD_LIBRARY_PATH and cannot accidentally
    # bind to a different libpython on the host. That is the entire class of
    # problem that made the snap's OpenSSL collision so hard to pin down.
    # Not --enable-optimizations either: PGO roughly quadruples build time for a
    # gain that does not matter to a workload this I/O bound.
    ./configure --prefix="$PYTHON_HOME" \
                --with-ensurepip=install \
                --enable-loadable-sqlite-extensions >/dev/null
    make -j"$(nproc)" >/dev/null
    make install >/dev/null
  )
  PYTHON_BIN="$PYTHON_HOME/bin/python3.14"
  [ -x "$PYTHON_BIN" ] || { echo "FATAL: CPython build produced no $PYTHON_BIN" >&2; exit 1; }

  # configure silently omits any stdlib module whose -dev headers were missing,
  # so verify the ones frappe actually needs rather than trusting the build.
  # ssl -> https and password hashing, sqlite3/lzma/bz2 -> backups,
  # ctypes -> several C extensions, readline -> `bench console`.
  "$PYTHON_BIN" - <<'PY'
import sys
missing = []
for mod in ("ssl", "sqlite3", "lzma", "bz2", "zlib", "ctypes",
            "readline", "hashlib", "uuid", "decimal"):
    try:
        __import__(mod)
    except Exception as exc:
        missing.append(f"{mod}: {exc}")
if missing:
    sys.exit("FATAL: CPython built without required stdlib modules:\n  "
             + "\n  ".join(missing))
import ssl
print(f"python {sys.version.split()[0]} ok, {ssl.OPENSSL_VERSION}")
PY

  PY_XY="$("$PYTHON_BIN" -c 'import sys; print("%d.%d" % sys.version_info[:2])')"
  # Frappe v16 pins >=3.14,<3.15 exactly. Anything else fails at `pip install -e
  # apps/frappe`, deep inside bench init, with a message that does not obviously
  # point back here.
  [ "$PY_XY" = "3.14" ] || { echo "FATAL: frappe v16 requires python 3.14, built $PY_XY" >&2; exit 1; }

  # --- service account -----------------------------------------------------
  # bench refuses to run as root, so the build needs a real unprivileged user
  # even though this container is thrown away.
  id frappe >/dev/null 2>&1 || useradd -m -s /bin/bash frappe
  mkdir -p /opt
  chown frappe:frappe /opt

  # --- node + yarn, staged inside the payload ------------------------------
  # Distribution node versions range from ancient to absent, and frappe's asset
  # build is sensitive to both. Bundle a known-good runtime under the bench.
  NODE_STAGE=/opt/.node-stage
  ARCH="$(uname -m)"
  case "$ARCH" in
    x86_64)  NODE_ARCH=x64 ;;
    aarch64) NODE_ARCH=arm64 ;;
    *) echo "FATAL: unsupported arch $ARCH" >&2; exit 1 ;;
  esac
  mkdir -p "$NODE_STAGE"
  curl -fsSL "https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-${NODE_ARCH}.tar.xz" \
    | tar -xJf - --strip-components=1 -C "$NODE_STAGE"
  export PATH="$NODE_STAGE/bin:$PATH"
  npm install -g yarn --prefix "$NODE_STAGE"
  chown -R frappe:frappe "$NODE_STAGE"

  # --- bench init at the final path ----------------------------------------
  rm -rf "$BENCH_DIR"
  # /tmp/bench-bootstrap/bin MUST be on PATH: the last step of `bench init` is
  # `bench build`, which bench runs by shelling out to the literal string
  # "bench". Without it the whole init fails at the very end with
  # `FileNotFoundError: [Errno 2] No such file or directory: 'bench'` -- after
  # twenty minutes of work, and then rolls it all back.
  #
  # npm_config_python points node-gyp at the system python3 (a build-only
  # dependency; the shipped venv uses the bundled 3.14). Some of frappe's
  # transitive node modules have no arm64 prebuild and fall back to compiling,
  # and node-gyp refuses to run without a python it recognises.
  runuser -u frappe -- env \
    PATH="/tmp/bench-bootstrap/bin:$NODE_STAGE/bin:/usr/local/bin:/usr/bin:/bin" \
    HOME=/home/frappe \
    npm_config_python=/usr/bin/python3 \
    bash -euxo pipefail -c "
      $PYTHON_BIN -m venv /tmp/bench-bootstrap
      /tmp/bench-bootstrap/bin/pip install --quiet --upgrade pip setuptools wheel
      /tmp/bench-bootstrap/bin/pip install --quiet 'frappe-bench==$BENCH_CLI_VERSION'

      yarn config set ignore-engines true || true

      cd /opt
      /tmp/bench-bootstrap/bin/bench init \
        --frappe-branch '$FRAPPE_BRANCH' \
        --python '$PYTHON_BIN' \
        --no-backups \
        --skip-redis-config-generation \
        --verbose \
        frappe-bench

      # The shipped bench must carry its own CLI: /opt/frappe-bench/env/bin/bench
      # is what every systemd unit invokes.
      #
      # NOT pinned to click<8.2. That pin was carried over from the snap and is
      # actively harmful here: bench init installs the Click 8.3.x that frappe
      # v16 requires (~=8.3.1), and the pin then DOWNGRADED it to 8.1.8, leaving
      # 'frappe 16.31.0 requires Click~=8.3.1, but you have click 8.1.8'.
      $BENCH_DIR/env/bin/pip install --quiet 'frappe-bench==$BENCH_CLI_VERSION'

      # frappe-bench declares click~=8.2.0 and frappe v16 declares click~=8.3.1.
      # Those cannot both be satisfied -- no click version exists in both ranges.
      # frappe is the application that actually runs under gunicorn and the
      # workers, so it wins; bench's constraint is the conservative one and its
      # CLI surface is exercised by test-native-package.sh.
      $BENCH_DIR/env/bin/pip install --quiet --upgrade 'click~=8.3.1'
    "

  # --- extra apps ----------------------------------------------------------
  for app in $EXTRA_APPS; do
    echo "==> bench get-app $app"
    runuser -u frappe -- env \
      PATH="$NODE_STAGE/bin:$BENCH_DIR/env/bin:/usr/local/bin:/usr/bin:/bin" \
      HOME=/home/frappe \
      bash -euxo pipefail -c "
        cd $BENCH_DIR
        $BENCH_DIR/env/bin/bench get-app --branch '$FRAPPE_BRANCH' '$app'
      "
  done

  # --- move node into the payload ------------------------------------------
  rm -rf "$BENCH_DIR/node"
  mv "$NODE_STAGE" "$BENCH_DIR/node"
  chown -R frappe:frappe "$BENCH_DIR/node"

  # --- bench layout guarantees ---------------------------------------------
  # is_bench_directory() checks apps, sites, config, logs AND config/pids. bench
  # init normally creates them all, but an empty config/pids has been dropped by
  # archive round-trips before, and its absence makes `bench worker` fail with
  # the deeply unhelpful "No such command 'worker'".
  mkdir -p "$BENCH_DIR/config/pids" "$BENCH_DIR/logs" "$BENCH_DIR/sites"
  touch "$BENCH_DIR/config/pids/.keep" "$BENCH_DIR/logs/.keep"

  for f in sites/apps.txt sites/apps.json; do
    [ -f "$BENCH_DIR/$f" ] || { echo "FATAL: $f missing after bench init" >&2; exit 1; }
  done

  # frappe must be the FIRST line of apps.txt. `bench get-app` appends, and if an
  # app ends up ahead of frappe the asset bundles load in the wrong order and the
  # desk comes up broken. This already bit the container images once (commit
  # bd45c9f); enforce it here rather than rediscovering it per packaging format.
  APPS_TXT="$BENCH_DIR/sites/apps.txt"
  if [ "$(head -1 "$APPS_TXT")" != "frappe" ]; then
    echo "==> reordering apps.txt to put frappe first"
    { echo frappe; grep -vx frappe "$APPS_TXT" || true; } > "$APPS_TXT.new"
    mv "$APPS_TXT.new" "$APPS_TXT"
  fi
  # bench writes apps.txt without a trailing newline.
  [ -z "$(tail -c1 "$APPS_TXT")" ] || echo >> "$APPS_TXT"
  echo "--- sites/apps.txt ---"; cat "$APPS_TXT"

  # --- replace bench's build-time common_site_config.json ------------------
  # bench init leaves its stock defaults here: redis on 11000/13000, which exist
  # nowhere on a native install. Overwrite with the config the package ships, so
  # the baked copy and the installed copy cannot drift.
  cp /src/scripts/templates/common_site_config.json \
     "$BENCH_DIR/sites/common_site_config.json"

  # --- relativise asset symlinks -------------------------------------------
  # Built at the final path these are already valid, but a relative link is
  # valid under relocation too, and costs nothing.
  ASSETS="$BENCH_DIR/sites/assets"
  if [ -d "$ASSETS" ]; then
    for link in "$ASSETS"/*; do
      [ -L "$link" ] || continue
      target="$(readlink "$link")"
      case "$target" in
        "$BENCH_DIR"/*)
          rel="${target#"$BENCH_DIR/"}"
          ln -sfn "../../$rel" "$link"
          echo "relinked assets/$(basename "$link") -> ../../$rel"
          ;;
      esac
    done
  fi

  # --- assertions ----------------------------------------------------------
  # Only sites/ and env/bin are asserted hard. node_modules legitimately
  # contains dangling links (optional peer deps), so those are reported, not
  # fatal -- a build that fails on someone else's package layout is worse than
  # one that tells you about it.
  if find "$BENCH_DIR/sites" "$BENCH_DIR/env/bin" -xtype l -print | grep -q .; then
    echo "FATAL: dangling symlinks under sites/ or env/bin:" >&2
    find "$BENCH_DIR/sites" "$BENCH_DIR/env/bin" -xtype l -print >&2
    exit 1
  fi
  DANGLING="$(find "$BENCH_DIR" -xtype l -printf '%p\n' 2>/dev/null | head -20 || true)"
  [ -z "$DANGLING" ] || { echo "NOTE: dangling symlinks elsewhere in the payload:"; echo "$DANGLING"; }

  # The venv must work through its own interpreter link, and its base_prefix must
  # be the BUNDLED interpreter at its final path -- if it points anywhere that
  # only exists in this container, the package is broken on arrival.
  "$BENCH_DIR/env/bin/python" -c 'import sys, ssl, sqlite3, lzma, ctypes; print("venv ok:", sys.version.split()[0], "base_prefix", sys.base_prefix)'
  VENV_BASE="$("$BENCH_DIR/env/bin/python" -c 'import sys; print(sys.base_prefix)')"
  [ "$VENV_BASE" = "$PYTHON_HOME" ] || {
    echo "FATAL: venv base_prefix is '$VENV_BASE', expected '$PYTHON_HOME'." >&2
    echo "       pyvenv.cfg would point outside the package." >&2; exit 1; }
  "$BENCH_DIR/env/bin/bench" --version

  # --- dependency consistency -----------------------------------------------
  # A pip resolver conflict is a WARNING, not an error, so a mismatched
  # dependency ships happily and fails at runtime. Assert that frappe's own
  # declared requirements are satisfied by what is installed -- frappe is what
  # gunicorn and the workers actually import.
  "$BENCH_DIR/env/bin/python" - <<'PY'
import sys
from importlib.metadata import distribution, version
from packaging.requirements import Requirement

unmet = []
for raw in distribution("frappe").requires or []:
    req = Requirement(raw)
    if req.marker and not req.marker.evaluate():
        continue
    try:
        have = version(req.name)
    except Exception:
        unmet.append(f"{req.name}: NOT INSTALLED (frappe needs {req.specifier})")
        continue
    if req.specifier and not req.specifier.contains(have, prereleases=True):
        unmet.append(f"{req.name}: have {have}, frappe needs {req.specifier}")

if unmet:
    sys.exit("FATAL: frappe's dependencies are not satisfied:\n  " + "\n  ".join(unmet))
print("frappe dependency check: OK "
      f"(click {version('click')}, semantic-version {version('semantic-version')})")
PY

  # Informational only: bench itself declares click~=8.2.0 and will be reported
  # as conflicting here. That is expected and deliberate -- see the install step.
  echo "--- pip check (the bench/click conflict below is expected) ---"
  "$BENCH_DIR/env/bin/pip" check || true
  echo "--- pyvenv.cfg ---"; cat "$BENCH_DIR/env/pyvenv.cfg"

  # --- runtime library dependencies ----------------------------------------
  # The bundled interpreter links against THIS distribution's OpenSSL, libffi,
  # sqlite and so on. Rather than hardcoding package names -- which differ per
  # release, and where Ubuntu 24.04's t64 renames (libssl3t64, libreadline8t64)
  # would silently produce an uninstallable package -- ask the package manager
  # which packages actually own the libraries the interpreter loads.
  echo "==> resolving runtime library dependencies"
  # Skip anything under $PYTHON_HOME: those are the interpreter's OWN modules,
  # which by definition no package owns. Leaving them in makes dpkg -S / rpm -qf
  # exit non-zero, xargs propagate 123, and -o pipefail kill the build after
  # twenty-five minutes of successful work.
  SO_LIST="$(
    { ldd "$PYTHON_BIN" 2>/dev/null
      find "$PYTHON_HOME/lib" -name '*.so' -exec ldd {} \; 2>/dev/null
    } | awk '/=> \//{print $3}' | grep -v "^$PYTHON_HOME" | sort -u
  )"

  # usrmerge: ldd reports /lib/<triplet>/libssl.so.3, but dpkg and rpm record
  # that same file as /usr/lib/<triplet>/libssl.so.3, so querying the ldd path
  # verbatim misses almost everything ("no path found matching pattern"). Ask
  # about both spellings and let the package manager match whichever it knows.
  SO_LIST="$(
    for p in $SO_LIST; do
      echo "$p"
      case "$p" in /lib/*|/lib64/*) echo "/usr$p" ;; esac
    done | sort -u
  )"
  # `|| true` on each lookup for the same reason: a file owned by no package is
  # normal (the dynamic loader itself, for one) and must not be fatal.
  RUNTIME_DEPS=""
  if [ "$FAMILY" = "debian" ]; then
    # dpkg -S has TWO output shapes and both must be handled:
    #   libssl3t64:arm64: /usr/lib/.../libssl.so.3          <- normal
    #   diversion by libreadline8t64 from: /lib/.../libreadline.so.8
    # For a diverted file the diversion lines may be all you get, so the package
    # name has to be pulled out of them too -- otherwise libreadline8t64 silently
    # drops off the dependency list. Naively cutting on ":" turns those lines
    # into the literal junk dependency "diversionbylibreadline8t64from".
    # Capture once, then make two independent passes. Piping into a { a; b; }
    # group does NOT give each command its own copy of the input: they share one
    # stdin, the first drains it, and the second silently sees nothing -- which
    # produced a dependency list containing only the diverted package.
    DPKG_OUT="$( { echo "$SO_LIST" | xargs -r dpkg -S 2>/dev/null || true; } )"
    RAW="$( { echo "$DPKG_OUT" | sed -n 's/^diversion by \([^ ]*\) .*/\1/p'
              echo "$DPKG_OUT" | grep -v '^diversion' | cut -d: -f1 \
                | tr ',' '\n' | tr -d ' '; } )"
  else
    RAW="$( { echo "$SO_LIST" | xargs -r rpm -qf --queryformat '%{NAME}\n' 2>/dev/null || true; } \
      | grep -v 'not owned' )"
  fi

  # Only keep names the package manager actually recognises. Anything else is a
  # parsing artefact, and a bogus name produces a package that cannot install.
  for pkg in $(echo "$RAW" | grep -v '^$' | sort -u); do
    if [ "$FAMILY" = "debian" ]; then
      dpkg-query -W "$pkg" >/dev/null 2>&1 || continue
    else
      rpm -q "$pkg" >/dev/null 2>&1 || continue
    fi
    RUNTIME_DEPS="$RUNTIME_DEPS$pkg "
  done
  echo "    -> $RUNTIME_DEPS"
  # The interpreter links against libc at the very least, so an empty list means
  # the resolution broke rather than that there is nothing to declare.
  [ -n "$RUNTIME_DEPS" ] || { echo "FATAL: resolved no runtime dependencies at all" >&2; exit 1; }
  for pkg in $RUNTIME_DEPS; do
    case "$pkg" in
      [a-z0-9]*) ;;
      *) echo "FATAL: '$pkg' is not a valid package name -- dependency parsing is broken" >&2
         exit 1 ;;
    esac
  done

  # --- slim ----------------------------------------------------------------
  rm -rf /home/frappe/.cache /root/.cache /tmp/bench-bootstrap
  find "$BENCH_DIR" -name '__pycache__' -type d -prune -exec rm -rf {} + 2>/dev/null || true
  find "$BENCH_DIR" -name '*.pyc' -delete 2>/dev/null || true

  chown -R frappe:frappe "$BENCH_DIR"
  # Group-shared, never world-writable -- the Containerfile's `chmod -R ug+rwX`
  # with frappe:frappe in place of 1001:0.
  chmod -R ug+rwX,o-w "$BENCH_DIR"

  # --- emit ----------------------------------------------------------------
  FRAPPE_VER="$(sed -n "s/^__version__ *= *['\"]\\([^'\"]*\\)['\"].*/\\1/p" "$BENCH_DIR/apps/frappe/frappe/__init__.py" | head -1)"
  SLUG="$(echo "$DISTRO" | tr ':' '-')"

  cat > "/out/payload-${SLUG}-${ARCH}.env" <<EOF
DISTRO=$DISTRO
FAMILY=$FAMILY
ARCH=$ARCH
PYTHON_BIN=$PYTHON_BIN
PYTHON_HOME=$PYTHON_HOME
PYTHON_XY=$PY_XY
PYTHON_VERSION=$PYTHON_VERSION
RUNTIME_DEPS="$RUNTIME_DEPS"
NODE_VERSION=$NODE_VERSION
FRAPPE_BRANCH=$FRAPPE_BRANCH
FRAPPE_VERSION=${FRAPPE_VER:-unknown}
EXTRA_APPS="$EXTRA_APPS"
BENCH_CLI_VERSION=$BENCH_CLI_VERSION
EOF

  # Both trees, in one archive: the bench, and the interpreter it was built
  # against. Shipping them separately would let a partial upgrade leave a venv
  # pointing at an interpreter that is no longer there.
  echo "==> packing payload (this takes a minute)"
  tar --numeric-owner --owner=0 --group=0 \
      -C / -czf "/out/payload-${SLUG}-${ARCH}.tar.gz" \
      opt/frappe-bench "${PYTHON_HOME#/}"

  ls -lh "/out/payload-${SLUG}-${ARCH}.tar.gz"
  echo "==> done"
  exit 0
fi

# ---------------------------------------------------------------------------
# Outer -- spawn the build container
# ---------------------------------------------------------------------------
DISTRO="ubuntu:24.04"
FRAPPE_BRANCH="version-16"
EXTRA_APPS=""
OUTPUT="./dist"
ENGINE=""
PYTHON_BIN=""

while [ $# -gt 0 ]; do
  case "$1" in
    --distro)        DISTRO="$2"; shift 2 ;;
    --frappe-branch) FRAPPE_BRANCH="$2"; shift 2 ;;
    --apps)          EXTRA_APPS="$2"; shift 2 ;;
    --output)        OUTPUT="$2"; shift 2 ;;
    --engine)        ENGINE="$2"; shift 2 ;;
    --python)        PYTHON_BIN="$2"; shift 2 ;;
    -h|--help)
      sed -n '2,30p' "$0"; exit 0 ;;
    *) echo "unknown option '$1'" >&2; exit 2 ;;
  esac
done

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

if [ -z "$ENGINE" ]; then
  if command -v podman >/dev/null 2>&1; then ENGINE=podman
  elif command -v docker >/dev/null 2>&1; then ENGINE=docker
  else echo "FATAL: neither podman nor docker found" >&2; exit 1; fi
fi

mkdir -p "$OUTPUT"
OUTPUT_ABS="$(cd "$OUTPUT" && pwd)"

# :z relabels for SELinux (mandatory on Fedora/RHEL hosts with podman); docker
# accepts the same suffix.
echo "==> $ENGINE run $DISTRO -- building bench payload at $BENCH_DIR"
exec "$ENGINE" run --rm \
  -v "$ROOT_DIR":/src:ro,z \
  -v "$OUTPUT_ABS":/out:z \
  -e VYBENCH_BUILD_INNER=1 \
  -e DISTRO="$DISTRO" \
  -e FRAPPE_BRANCH="$FRAPPE_BRANCH" \
  -e EXTRA_APPS="$EXTRA_APPS" \
  -e PYTHON_BIN="$PYTHON_BIN" \
  "$DISTRO" \
  bash /src/scripts/build-bench-payload.sh
