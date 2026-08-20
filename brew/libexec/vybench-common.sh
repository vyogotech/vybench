#!/bin/bash
# Shared bootstrap sourced by every vybench wrapper on macOS.
#
# The macOS equivalent of snap/local/bin/snap-common.sh.
# Uses HOMEBREW_PREFIX instead of $SNAP, and ~/Library/Application Support/vybench
# or $(brew --prefix)/var/vybench for data directories.

# ---------------------------------------------------------------------------
# Homebrew prefix detection (works on both Apple Silicon and Intel)
# ---------------------------------------------------------------------------
if [ -z "$HOMEBREW_PREFIX" ]; then
  if [ -x /opt/homebrew/bin/brew ]; then
    HOMEBREW_PREFIX=/opt/homebrew
  elif [ -x /usr/local/bin/brew ]; then
    HOMEBREW_PREFIX=/usr/local
  else
    HOMEBREW_PREFIX="$(brew --prefix 2>/dev/null || echo /opt/homebrew)"
  fi
fi

# Auto-detect vybench instance name from LIBEXEC_DIR
if [ -z "$VYBENCH_NAME" ]; then
  if [[ "$LIBEXEC_DIR" == *"vybench-local"* ]]; then
    VYBENCH_NAME="vybench-local"
  else
    VYBENCH_NAME="vybench"
  fi
fi

VYBENCH_LIBEXEC="${VYBENCH_LIBEXEC:-$HOMEBREW_PREFIX/opt/$VYBENCH_NAME/libexec}"
VYBENCH_ETC="${VYBENCH_ETC:-$HOMEBREW_PREFIX/etc/$VYBENCH_NAME}"
VYBENCH_VAR="${VYBENCH_VAR:-$HOMEBREW_PREFIX/var/$VYBENCH_NAME}"
VYBENCH_LOG="${VYBENCH_LOG:-$HOMEBREW_PREFIX/var/log/$VYBENCH_NAME}"
VYBENCH_RUN="${VYBENCH_RUN:-$HOMEBREW_PREFIX/var/run/$VYBENCH_NAME}"

# Where the live bench lives (writable, under var/)
BENCH_ROOT="${BENCH_ROOT:-$VYBENCH_VAR/bench}"
BENCH_PY="$BENCH_ROOT/env/bin/python3"
[ -x "$BENCH_PY" ] || BENCH_PY="$BENCH_ROOT/env/bin/python"
BENCH_CLI="$BENCH_ROOT/env/bin/bench"

# Homebrew-managed dependencies
MARIADB_PREFIX="$HOMEBREW_PREFIX/opt/mariadb"
REDIS_PREFIX="$HOMEBREW_PREFIX/opt/redis"
NODE_PREFIX="$HOMEBREW_PREFIX/opt/node"
PYTHON_PREFIX="$HOMEBREW_PREFIX/opt/python@3.14"

export PATH="$PYTHON_PREFIX/bin:$NODE_PREFIX/bin:$MARIADB_PREFIX/bin:$REDIS_PREFIX/bin:$BENCH_ROOT/env/bin:$HOMEBREW_PREFIX/bin:$PATH"

# Default python and build flags for compiling mysqlclient and frappe extensions
export PYTHON="$PYTHON_PREFIX/bin/python3.14"
export UV_PYTHON="$PYTHON_PREFIX/bin/python3.14"
export PKG_CONFIG_PATH="$MARIADB_PREFIX/lib/pkgconfig:${PKG_CONFIG_PATH:-}"
export MYSQLCLIENT_CFLAGS="-I$MARIADB_PREFIX/include/mysql"
export MYSQLCLIENT_LDFLAGS="-L$MARIADB_PREFIX/lib -lmariadb"
export MYSQL_CONFIG="$MARIADB_PREFIX/bin/mariadb_config"

# MariaDB socket — dedicated vybench socket
MYSQL_SOCKET="$VYBENCH_RUN/mysql.sock"
export MYSQL_UNIX_PORT="$MYSQL_SOCKET"

# Git safe.directory — avoids "dubious ownership" errors
export GIT_CONFIG_COUNT=1
export GIT_CONFIG_KEY_0=safe.directory
export GIT_CONFIG_VALUE_0='*'

# UTF-8 locale
export LC_ALL=en_US.UTF-8
export LANG=en_US.UTF-8

# ---------------------------------------------------------------------------
# bootstrap_dirs — create all runtime directories
# ---------------------------------------------------------------------------
bootstrap_dirs() {
  mkdir -p \
    "$VYBENCH_VAR/mariadb" \
    "$VYBENCH_VAR/redis" \
    "$VYBENCH_RUN" \
    "$VYBENCH_LOG" \
    "$BENCH_ROOT/logs" \
    "$BENCH_ROOT/config/pids" \
    "$BENCH_ROOT/sites" \
    2>/dev/null || true
}

# ---------------------------------------------------------------------------
# bootstrap_common — dirs + symlink apps/env from libexec into var/bench
# ---------------------------------------------------------------------------
bootstrap_common() {
  bootstrap_dirs

  # Link apps/ and env/ from the read-only libexec into the writable bench,
  # but only while they are still symlinks (don't clobber a materialised bench).
  local SNAP_SRC="$VYBENCH_LIBEXEC/frappe-bench"
  for tree in env apps; do
    if [ ! -e "$BENCH_ROOT/$tree" ] || [ -L "$BENCH_ROOT/$tree" ]; then
      ln -sfn "$SNAP_SRC/$tree" "$BENCH_ROOT/$tree" 2>/dev/null || true
    fi
  done

  # Seed apps.txt so bench discovers frappe/erpnext subcommands
  for seed in apps.txt apps.json; do
    if [ ! -f "$BENCH_ROOT/sites/$seed" ] && [ -f "$SNAP_SRC/sites/$seed" ]; then
      cp "$SNAP_SRC/sites/$seed" "$BENCH_ROOT/sites/$seed" 2>/dev/null || true
    fi
  done

  # sites/assets symlink
  if [ ! -e "$BENCH_ROOT/sites/assets" ] || [ -L "$BENCH_ROOT/sites/assets" ]; then
    ln -sfn "$SNAP_SRC/sites/assets" "$BENCH_ROOT/sites/assets" 2>/dev/null || true
  fi

  # Seed common_site_config.json
  if [ ! -f "$BENCH_ROOT/sites/common_site_config.json" ]; then
    cp "$VYBENCH_ETC/common_site_config.json" \
       "$BENCH_ROOT/sites/common_site_config.json" 2>/dev/null || true
  fi
}

# ---------------------------------------------------------------------------
# materialise_bench — copy apps/ and env/ out of libexec into var/bench
# (developer mode, ~1 GB, done once)
# ---------------------------------------------------------------------------
materialise_bench() {
  local SRC="$VYBENCH_LIBEXEC/frappe-bench"

  for tree in apps env; do
    if [ ! -d "$BENCH_ROOT/$tree" ] || [ -L "$BENCH_ROOT/$tree" ]; then
      echo "vybench: materialising $tree/ (this takes a moment)..."
      rm -rf "$BENCH_ROOT/$tree"
      cp -a "$SRC/$tree" "$BENCH_ROOT/$tree"
    fi
  done

  # Copy assets (contains relative symlinks)
  if [ -L "$BENCH_ROOT/sites/assets" ]; then
    rm -f "$BENCH_ROOT/sites/assets"
    cp -a "$SRC/sites/assets" "$BENCH_ROOT/sites/assets"
  fi

  # Re-point venv interpreter to the Homebrew python
  local VENV_BIN="$BENCH_ROOT/env/bin"
  local PY_BIN="$PYTHON_PREFIX/bin/python3.14"
  if [ -d "$VENV_BIN" ]; then
    ln -sfn "$PY_BIN" "$VENV_BIN/python3.14"
    ln -sfn python3.14 "$VENV_BIN/python3"
    ln -sfn python3.14 "$VENV_BIN/python"
    find "$VENV_BIN" -type f -exec sed -i '' '1s|^#!/.*/python.*|#!/usr/bin/env python3|' {} + 2>/dev/null || true
  fi

  # Rewrite pyvenv.cfg to point at Homebrew python
  local PYVENV="$BENCH_ROOT/env/pyvenv.cfg"
  if [ -f "$PYVENV" ]; then
    sed -i '' \
      -e "s|^home = .*|home = $PYTHON_PREFIX/bin|" \
      -e "s|^executable = .*|executable = $PY_BIN|" \
      -e "s|^base-executable = .*|base-executable = $PY_BIN|" \
      "$PYVENV"
  fi

  # Rewrite pth/egg-link build paths to $BENCH_ROOT
  local SP="$BENCH_ROOT/env/lib/python3.14/site-packages"
  if [ -d "$SP" ]; then
    find "$SP" -type f \( -name "*.pth" -o -name "*.py" -o -name "*.egg-link" \) \
      -exec sed -i '' "s|$VYBENCH_LIBEXEC/frappe-bench|$BENCH_ROOT|g" {} + 2>/dev/null || true
  fi
}

# ---------------------------------------------------------------------------
# get_mode — read mode config from ~/.vybench/config or env
# ---------------------------------------------------------------------------
VYBENCH_CONFIG_FILE="${XDG_CONFIG_HOME:-$HOME/.config}/vybench/config"

get_mode() {
  local m
  m="$(vybench_config_get mode 2>/dev/null)" || m=""
  case "$m" in
    developer|dev) echo developer ;;
    *)             echo production ;;
  esac
}

vybench_config_get() {
  local key="$1"
  if [ -f "$VYBENCH_CONFIG_FILE" ]; then
    grep "^${key}=" "$VYBENCH_CONFIG_FILE" 2>/dev/null | cut -d= -f2- | head -1
  fi
}

vybench_config_set() {
  local key="$1" val="$2"
  mkdir -p "$(dirname "$VYBENCH_CONFIG_FILE")"
  if [ -f "$VYBENCH_CONFIG_FILE" ] && grep -q "^${key}=" "$VYBENCH_CONFIG_FILE" 2>/dev/null; then
    sed -i '' "s|^${key}=.*|${key}=${val}|" "$VYBENCH_CONFIG_FILE"
  else
    echo "${key}=${val}" >> "$VYBENCH_CONFIG_FILE"
  fi
}

# ---------------------------------------------------------------------------
# site_conf — read a key from common_site_config.json
# ---------------------------------------------------------------------------
site_conf() {
  local key="$1"
  "$BENCH_PY" - "$key" 2>/dev/null <<'PYEOF' || true
import json, os, sys
path = os.path.join(os.environ.get("BENCH_ROOT", ""), "sites", "common_site_config.json")
try:
    with open(path) as fh:
        print(json.load(fh).get(sys.argv[1], "") or "")
except Exception:
    print("")
PYEOF
}

# ---------------------------------------------------------------------------
# require_mariadb / require_redis — wait for datastores to be ready
# ---------------------------------------------------------------------------
wait_for_socket() {
  local sock="$1" label="$2" attempts="${3:-30}"
  local i=0
  while [ $i -lt "$attempts" ]; do
    [ -S "$sock" ] && return 0
    sleep 1
    i=$((i + 1))
  done
  echo "vybench: timed out waiting for $label socket at $sock" >&2
  return 1
}

wait_for_tcp() {
  local host="$1" port="$2" label="$3" attempts="${4:-30}"
  local i=0
  while [ $i -lt "$attempts" ]; do
    nc -z "$host" "$port" 2>/dev/null && return 0
    sleep 1
    i=$((i + 1))
  done
  echo "vybench: timed out waiting for $label on $host:$port" >&2
  return 1
}
