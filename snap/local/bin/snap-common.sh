#!/bin/bash
# Shared bootstrap sourced by every vybench wrapper.
#
# snapd starts all services as root. Nothing in this snap is allowed to STAY
# root: each wrapper calls run_as_daemon, which drops to the snap_daemon system
# account (created by snapd via `system-usernames`) before exec'ing the real
# process. Dropping privileges also keeps bench happy -- bench refuses to run as
# root and its change_uid() would otherwise try to setuid to a `frappe` user that
# does not exist on the host.

DAEMON_USER=snap_daemon

SNAP_NAME="${SNAP_INSTANCE_NAME:-vybench}"

# $SNAP is revision-specific (/snap/vybench/x11). Anything persisted into
# $SNAP_COMMON and expected to survive a refresh must point at the revision
# -stable `current` symlink instead, or it dangles after the next upgrade.
SNAP_STABLE="/snap/$SNAP_NAME/current"

# Where the live apps/ and env/ actually are.
#
# Production keeps them as symlinks into the read-only squashfs: immutable, and
# `snap revert` rolls them back atomically. A developer install materialises them
# into $SNAP_COMMON (see materialise_bench) so that get-app / new-app / source
# edits / pip install / bench build all work like a normal bench. Detect which
# layout is in play rather than assuming, so both modes use one code path.
#
# LIVE_BENCH is the writable bench everything here operates on. It is resolved
# in the same order as the Homebrew wrapper and the Go CLI (BC-1):
# 1. VYBENCH_BENCH (or BENCH_ROOT): explicit override, e.g. from the TUI
# 2. the current directory, when it is a bench (sites/ and apps/)
# 3. the current-bench symlink (the multi-bench active bench)
# 4. $SNAP_COMMON/bench (the single bench of a pre-multi-bench install)
if [ -n "${VYBENCH_BENCH:-}" ]; then
  LIVE_BENCH="$VYBENCH_BENCH"
elif [ -n "${BENCH_ROOT:-}" ] && [ "$BENCH_ROOT" != "$SNAP/opt/frappe-bench" ]; then
  LIVE_BENCH="$BENCH_ROOT"
elif [ -d ./sites ] && [ -d ./apps ]; then
  LIVE_BENCH="$(pwd -P)"
elif [ -L "$SNAP_COMMON/current-bench" ]; then
  LIVE_BENCH="$(readlink -f "$SNAP_COMMON/current-bench")"
else
  LIVE_BENCH="$SNAP_COMMON/bench"
fi
export LIVE_BENCH

# A bench whose env/ is a real directory (materialised, or built by
# `bench init` for another Frappe release) runs its own interpreter; one that
# still links the squashfs, or has not been bootstrapped yet, runs the payload's.
if [ -d "$LIVE_BENCH/env" ] && [ ! -L "$LIVE_BENCH/env" ]; then
  BENCH_ROOT="$LIVE_BENCH"                # materialised, writable
else
  BENCH_ROOT="$SNAP/opt/frappe-bench"     # squashfs, read-only
fi
BENCH_PY="$BENCH_ROOT/env/bin/python3.14"
[ -x "$BENCH_PY" ] || BENCH_PY="$BENCH_ROOT/env/bin/python3"
export BENCH_CLI="$BENCH_ROOT/env/bin/bench"

export PATH="$SNAP/usr/sbin:$SNAP/usr/bin:$SNAP/sbin:$SNAP/bin:$SNAP/usr/lib/postgresql/16/bin:$SNAP/usr/lib/postgresql/15/bin:$BENCH_ROOT/env/bin:$PATH"
export LD_LIBRARY_PATH="$SNAP/usr/lib/x86_64-linux-gnu:$SNAP/usr/lib/aarch64-linux-gnu:$SNAP/usr/lib:$SNAP/lib/x86_64-linux-gnu:$SNAP/lib/aarch64-linux-gnu:$SNAP/lib:$LD_LIBRARY_PATH"
# libmagic's compiled-in database path is /usr/lib/file/magic.mgc, and inside the
# snap /usr is the base's, which has no such file -- so the file(1) staged above
# would fail with "cannot open magic file". `bench restore` calls file(1) on
# every backup it restores. Guarded so a build without it is left alone.
[ -f "$SNAP/usr/lib/file/magic.mgc" ] && export MAGIC="$SNAP/usr/lib/file/magic.mgc"
export PYTHONPATH="$BENCH_ROOT/apps/frappe:$BENCH_ROOT/env/lib/python3.14/site-packages"
# The socket lives OUTSIDE the data directory on purpose. The datadir holds the
# actual database files and stays private to snap_daemon (0770); the socket
# directory is world-traversable so any local user can connect without being
# added to a group. Connecting is not authorisation -- MariaDB still demands the
# generated root password -- so an open socket costs nothing.
# MYSQL_UNIX_PORT doubles as `bench new-site`'s --db-socket envvar fallback
# (frappe's new-site click option declares envvar="MYSQL_UNIX_PORT"). Exporting
# it whenever this merely isn't the bundled-postgres snap variant still leaks
# vybench's own MariaDB socket into a bench whose db_type is postgres (talking
# to an external server over TCP): libpq then dials
# "<mariadb-socket>/.s.PGSQL.<port>", which is not a directory. Check the
# active bench's own config, not just the snap variant.
_vb_active_db_type() {
  local cfg="$LIVE_BENCH/sites/common_site_config.json"
  [ -f "$cfg" ] || { echo mariadb; return; }
  [ -x "$BENCH_PY" ] || { echo mariadb; return; }
  "$BENCH_PY" -c 'import json,sys
try:
    with open(sys.argv[1]) as fh:
        print(json.load(fh).get("db_type") or "mariadb")
except Exception:
    print("mariadb")' "$cfg" 2>/dev/null
}
if [ ! -f "$SNAP/bin/postgres-wrapper" ] && [ ! -d "$SNAP/usr/lib/postgresql" ] && [ "$(_vb_active_db_type)" != "postgres" ]; then
  export MYSQL_UNIX_PORT="$SNAP_COMMON/run/mysql.sock"
fi
export PGHOST="$SNAP_COMMON/run"
export PGPORT="5432"
export PGSHAREDIR="$SNAP/usr/share/postgresql/16"

# The bench baked into $SNAP has its own sites/common_site_config.json left over
# from `bench init`, carrying bench's DEFAULT ports (redis_queue 11000,
# redis_cache 13000) and frappe_user=frappe. frappe's node code resolves that
# file relative to __dirname, so socketio would read the build-time config and
# dial redis on 11000. Point every process at the live writable bench instead.
export FRAPPE_BENCH_ROOT="$LIVE_BENCH"

# Git refuses to touch a repository owned by a different UID ("detected dubious
# ownership"). The bench tree is owned by snap_daemon and shared with the
# developer through the group, so filesystem permissions are fine but git's check
# is stricter -- it wants an owner match. Without this, every git-backed bench
# command (update, switch-to-branch, get-app) fails on a developer install.
#
# Injected through the environment rather than by writing to the user's
# ~/.gitconfig: it applies only to git processes bench itself spawns, and leaves
# the developer's own git configuration untouched.
export GIT_CONFIG_COUNT=1
export GIT_CONFIG_KEY_0=safe.directory
export GIT_CONFIG_VALUE_0='*'
# snap_daemon (and AppArmor) cannot read the host's /etc/gitconfig. Without
# this, every `bench get-app` / `git clone` dies with:
#   warning: unable to access '/etc/gitconfig': Permission denied
#   fatal: unknown error occurred while reading the configuration files
export GIT_CONFIG_NOSYSTEM=1

# The tree is shared between the snap_daemon services and the host user who runs
# `vybench.bench`, and neither can chown the other's files.
#
# This mirrors the container image's approach (Containerfile: `chown -R 1001:0 .
# && chmod -R ug+rwX .`) -- shared through GROUP ownership, never world-writable.
# The container can safely use group 0 because the container is the security
# boundary; on a host, putting a human user in the root group is privilege
# escalation, so the shared group here is snap_daemon instead.
#
# 002 keeps new files group-writable and world-unreadable.
umask 002

# Set a mode, and the setgid bit, as two calls.
#
# snapd's seccomp policy denies any chmod whose mode carries S_ISGID:
#
#   ~chmod - |S_ISGID
#
# The whole syscall is refused with EPERM, so `chmod u+rwx,g+rwxs,o-rwx DIR`
# does not "apply what it can" -- it applies NOTHING, silently, because every
# caller here ends in `|| true`. Asking for the permission bits and the setgid
# bit separately means the part that can be set always is.
#
# The setgid call is expected to fail inside the snap and is kept only for an
# unconfined build. Measured on the appliance: mkdir(2) cannot set the bit
# either (the kernel masks a new directory's mode to S_IRWXUGO|S_ISVTX), so a
# directory inside a strict snap gets setgid only by inheriting it. That costs
# nothing here -- umask 002 above, plus a single service account, already keep
# new files group-writable.
share_mode() {
  local mode="$1"; shift
  chmod "$mode" "$@" 2>/dev/null || true
  chmod g+s "$@" 2>/dev/null || true
}

# Datastore directories. These belong to snap_daemon in BOTH modes, because
# mariadbd and redis always run as that account. The bench tree is deliberately
# not touched here -- see bootstrap_bench.
bootstrap_datastores() {
  mkdir -p "$SNAP_COMMON/mariadb" "$SNAP_COMMON/postgres" "$SNAP_COMMON/redis" "$SNAP_COMMON/run" 2>/dev/null || true

  if [ "$(id -u)" = "0" ]; then
    chown "$DAEMON_USER:$DAEMON_USER" \
      "$SNAP_COMMON/mariadb" "$SNAP_COMMON/postgres" "$SNAP_COMMON/redis" "$SNAP_COMMON/run" 2>/dev/null || true
  fi

  # Database files stay private...
  share_mode u+rwx,g+rwx,o-rwx "$SNAP_COMMON/mariadb" "$SNAP_COMMON/postgres" "$SNAP_COMMON/redis"
  # ...but the socket directory is traversable, so `bench` works for any local
  # user with no group membership. Auth still gates actual database access.
  chmod 0755 "$SNAP_COMMON/run" 2>/dev/null || true
}

# Create the writable runtime layout of $LIVE_BENCH and hand it to snap_daemon.
bootstrap_common() {
  bootstrap_datastores
  # bench's is_bench_directory() requires ALL of paths_in_bench to exist:
  #   ("apps", "sites", "config", "logs", "config/pids")
  # Miss any one -- config/pids is the easy one to forget -- and bench decides it
  # is not in a bench, silently skips loading frappe's subcommands, and
  # `bench worker` dies with "No such command 'worker'".
  mkdir -p "$LIVE_BENCH/logs" "$LIVE_BENCH/config/pids" \
           "$LIVE_BENCH/sites" 2>/dev/null || true

  # Link apps/ and env/ at the squashfs -- but ONLY while they are still links.
  # Once materialise_bench has replaced them with real directories, `ln -sf` would
  # create the link *inside* the directory (bench/env/env -> ...), quietly
  # corrupting a developer bench every time a service restarts.
  for tree in env apps; do
    if [ ! -e "$LIVE_BENCH/$tree" ] || [ -L "$LIVE_BENCH/$tree" ]; then
      as_daemon ln -sfn "$SNAP_STABLE/opt/frappe-bench/$tree" \
              "$LIVE_BENCH/$tree" 2>/dev/null || true
    fi
  done

  # Seed the writable sites/ directory from the bench baked into the snap. bench
  # discovers installed apps through sites/apps.txt; without it even
  # `bench worker` fails with "No such command 'worker'", because frappe's
  # subcommands are never loaded. assets/ is symlinked rather than copied: it is
  # large, read-only, and version-locked to this squashfs revision.
  for seed in apps.txt apps.json; do
    if [ ! -f "$LIVE_BENCH/sites/$seed" ] && \
       [ -f "$SNAP/opt/frappe-bench/sites/$seed" ]; then
      as_daemon cp "$SNAP/opt/frappe-bench/sites/$seed" "$LIVE_BENCH/sites/$seed" 2>/dev/null || true
    fi
  done
  # Same guard as apps/env: leave a materialised (real) assets tree alone.
  if [ ! -e "$LIVE_BENCH/sites/assets" ] || [ -L "$LIVE_BENCH/sites/assets" ]; then
    as_daemon ln -sfn "$SNAP_STABLE/opt/frappe-bench/sites/assets" \
            "$LIVE_BENCH/sites/assets" 2>/dev/null || true
  fi

  # Seed from the static config shipped in the snap rather than generating JSON
  # here. The same file is baked over the bench's build-time config, so both
  # copies agree by construction.
  if [ ! -f "$LIVE_BENCH/sites/common_site_config.json" ]; then
    as_daemon cp "$SNAP/config/common_site_config.json" \
       "$LIVE_BENCH/sites/common_site_config.json" 2>/dev/null || true
    
    # Ensure db_type is postgres in common_site_config.json when running under postgres snap
    if [ -f "$SNAP/bin/postgres-wrapper" ]; then
      as_daemon "$SNAP/opt/frappe-bench/env/bin/python3.14" - "$LIVE_BENCH/sites/common_site_config.json" <<'PYEOF' || true
import json, sys
path = sys.argv[1]
with open(path) as fh:
    cfg = json.load(fh)
cfg["db_type"] = "postgres"
cfg["db_port"] = 5432
cfg["db_host"] = "127.0.0.1"
cfg.pop("db_socket", None)
with open(path, "w") as fh:
    json.dump(cfg, fh, indent=1)
PYEOF
    fi
  fi

  # Shared-write surface between the snap_daemon services and the CLI user.
  # Deliberately scoped: apps/ and env/ are excluded because on a materialised
  # developer bench they are ~1GB and recursing them on every one of six daemon
  # starts is seconds of pointless I/O. materialise_bench fixes their modes once.
  local shared="$LIVE_BENCH/sites $LIVE_BENCH/config \
                $LIVE_BENCH/logs"

  # Only claim the bench for snap_daemon when the managed app tier actually owns
  # it -- i.e. production. In developer mode those daemons are disabled and never
  # touch this tree, so leaving it owned by whoever ran `bench` means the
  # developer needs neither a group nor sudo, and git sees a repo it owns.
  if [ "$(id -u)" = "0" ] && [ "$(get_mode)" = "production" ]; then
    chown "$DAEMON_USER:$DAEMON_USER" "$LIVE_BENCH" 2>/dev/null || true
    # shellcheck disable=SC2086
    chown -R "$DAEMON_USER:$DAEMON_USER" $shared 2>/dev/null || true
  fi

  # ug+rwX, not 777: owner and the snap_daemon group get access, everyone else
  # gets nothing. g+s on directories makes new files inherit the group, so files
  # created by the CLI user stay writable by the daemons and vice versa -- without
  # it the sharing silently decays as soon as either side writes something new.
  share_mode u+rwx,g+rwx,o-rwx "$LIVE_BENCH"
  # shellcheck disable=SC2086
  chmod -R ug+rwX,o-rwx $shared 2>/dev/null || true
  # shellcheck disable=SC2086
  find $shared -type d -exec chmod g+s {} + 2>/dev/null || true
}

# Run a single command as snap_daemon if invoked by root, otherwise run directly.
# Used by hooks and helpers to modify files in $SNAP_COMMON/bench without hitting
# AppArmor dac_override denials (since root lacks DAC override in strict snaps).
as_daemon() {
  if [ "$(id -u)" = "0" ]; then
    setpriv --reuid="$DAEMON_USER" --regid="$DAEMON_USER" --clear-groups "$@"
  else
    "$@"
  fi
}

# Copy apps/ and env/ out of the read-only squashfs into $SNAP_COMMON, turning
# the install into an ordinary, fully writable bench (~1GB).
#
# This is what makes a developer install real: `bench update` and
# `bench switch-to-branch` git-pull into apps/ and pip-install into env/, and
# `bench get-app` / `new-app` / `build` all need to write there too. None of that
# is possible while those trees live in squashfs.
#
# Production deliberately does NOT do this: symlinks into the squashfs keep the
# install immutable and let `snap refresh` / `snap revert` be the upgrade path.
materialise_bench() {
  local B="${1:-$LIVE_BENCH}"
  local SRC="$SNAP/opt/frappe-bench"

  for tree in apps env; do
    if [ ! -d "$B/$tree" ] || [ -L "$B/$tree" ]; then
      echo "materialising $tree/ (this takes a moment)..."
      as_daemon rm -rf "$B/$tree"
      as_daemon cp -a "$SRC/$tree" "$B/$tree"
    fi
  done

  # sites/assets holds RELATIVE symlinks (../../apps/<app>/<app>/public), so once
  # copied they resolve against the materialised apps/ automatically.
  if [ -L "$B/sites/assets" ]; then
    as_daemon rm -f "$B/sites/assets"
    as_daemon cp -a "$SRC/sites/assets" "$B/sites/assets"
  fi

  # Ensure db_type is postgres in common_site_config.json when running under postgres snap
  if [ -f "$B/sites/common_site_config.json" ] && [ -f "$SNAP/bin/postgres-wrapper" ]; then
    as_daemon "$SNAP/opt/frappe-bench/env/bin/python3.14" - "$B/sites/common_site_config.json" <<'PYEOF' || true
import json, sys
path = sys.argv[1]
with open(path) as fh:
    cfg = json.load(fh)
cfg["db_type"] = "postgres"
cfg["db_port"] = 5432
cfg["db_host"] = "127.0.0.1"
cfg.pop("db_socket", None)
with open(path, "w") as fh:
    json.dump(cfg, fh, indent=1)
PYEOF
  fi

  # The venv's interpreter link is relative (../../../../usr/bin/python3.14),
  # which counts four levels from $SNAP/opt/frappe-bench/env/bin but lands on
  # /var/snap/vybench/usr/bin from the copied location -- a dangling link that
  # breaks every bench command. Re-point it absolutely at the bundled interpreter.
  # Also rewrite build-time shebangs (#!/build/vybench/...) to #!/usr/bin/env python3.
  if [ -d "$B/env/bin" ]; then
    as_daemon ln -sfn "$SNAP_STABLE/usr/bin/python3.14" "$B/env/bin/python3.14"
    as_daemon ln -sfn python3.14 "$B/env/bin/python3"
    as_daemon ln -sfn python3.14 "$B/env/bin/python"
    as_daemon find "$B/env/bin" -type f -exec sed -i "1s|^#!/.*/python.*|#!$B/env/bin/python3|" {} + 2>/dev/null || true
    [ -e "$B/env/bin/python3.14" ] || echo "WARNING: materialised venv python is dangling" >&2
  fi

  # Rewrite build-time part install paths in site-packages pth/egg-link files to point to $B
  if [ -d "$B/env/lib/python3.14/site-packages" ]; then
    SP="$B/env/lib/python3.14/site-packages"
    # First pass: rewrite explicit known build-time paths
    as_daemon find "$SP" -type f \( -name "*.pth" -o -name "*.py" -o -name "*.egg-link" \) \
      -exec sed -i \
        -e "s|/tmp/vybench/parts/frappe-bench/install/opt/frappe-bench|$B|g" \
        -e "s|/root/parts/frappe-bench/install/opt/frappe-bench|$B|g" \
        -e "s|$SNAP/opt/frappe-bench|$B|g" \
        {} + 2>/dev/null || true
    # Second pass: rewrite any remaining /snap/<name>/current/opt/frappe-bench references
    # (these are paths baked by snapcraft at pack time)
    SNAP_CURRENT_PREFIX="/snap/${SNAP_NAME}/current/opt/frappe-bench"
    as_daemon find "$SP" -type f \( -name "*.pth" -o -name "*.egg-link" \) \
      -exec sed -i "s|${SNAP_CURRENT_PREFIX}|$B|g" {} + 2>/dev/null || true
    # Belt-and-suspenders: forcibly rewrite the two known editable-install .pth files
    for APP in frappe erpnext hrms; do
      PTH="$SP/${APP}.pth"
      [ -f "$PTH" ] && sed -i "s|/snap/${SNAP_NAME}/current/opt/frappe-bench/apps/${APP}|$B/apps/${APP}|g" "$PTH" 2>/dev/null || true
    done
  fi

  # Patch frappe's popen() set_low_prio to safely catch ionice/nice exceptions.
  # Under snap confinement (and unprivileged snap_daemon execution), ioprio_set(2)
  # is blocked by AppArmor/seccomp, raising SubprocessError: Exception occurred in preexec_fn.
  FRAPPE_CMDS="$B/apps/frappe/frappe/commands/__init__.py"
  if [ -f "$FRAPPE_CMDS" ]; then
    python3 -c "
import re
path = '$FRAPPE_CMDS'
try:
    with open(path, 'r') as f:
        content = f.read()
    if 'def set_low_prio():' in content and 'try:' not in content.split('def set_low_prio():')[1].split('proc = subprocess.Popen')[0]:
        pattern = r'(def set_low_prio\(\):)(\n\s+import psutil[\s\S]*?)(?=\n\s+proc\s*=)'
        def wrap_try(m):
            lines = m.group(2).split('\n')
            indented = '\n'.join('\t' + line if line.strip() else line for line in lines)
            return m.group(1) + '\n\t\ttry:' + indented + '\n\t\texcept Exception:\n\t\t\tpass'
        new_content = re.sub(pattern, wrap_try, content)
        with open(path, 'w') as f:
            f.write(new_content)
except Exception:
    pass
" 2>/dev/null || true
  fi


  if [ "$(id -u)" = "0" ]; then
    chown -R "$DAEMON_USER:$DAEMON_USER" "$B" 2>/dev/null || true
  fi
  # Group-shared, not world-readable -- same rule as bootstrap_common.
  chmod -R ug+rwX,o-rwx "$B" 2>/dev/null || true
  find "$B" -type d -exec chmod g+s {} + 2>/dev/null || true
}

# In developer mode the bench belongs to the developer, so this never trips.
# In production the app daemons own it as snap_daemon, and a plain user cannot
# write to it. Test for actual writability rather than group membership -- that
# is the thing that matters, and it keeps quiet whenever access already works.
require_bench_access() {
  [ "$(id -u)" = "0" ] && return 0

  local probe="$LIVE_BENCH/sites"
  [ -d "$probe" ] || return 0            # nothing bootstrapped yet; nothing to check
  [ -w "$probe" ] && return 0            # already writable -- say nothing

  cat >&2 <<EOF
$SNAP_NAME: the bench at $LIVE_BENCH is owned by '$DAEMON_USER' and is not
writable by $(id -un).

This install is in production mode, where the services own the bench. Either:

  sudo $SNAP_NAME.bench $*
      run the command as the service account (no setup needed), or

  sudo usermod -aG $DAEMON_USER $(id -un)   # then re-login, or: newgrp $DAEMON_USER
      grant yourself permanent access, like docker's post-install step.

A developer install (snap set $SNAP_NAME mode=developer) needs neither.
EOF
  return 1
}

# Exec "$@" as snap_daemon. When already unprivileged (the bench CLI invoked by a
# host user) exec directly -- there is nothing to drop.
#
# --clear-groups, not --init-groups. Under strict confinement snapd's seccomp
# profile for a `system-usernames` snap permits setgroups(0, NULL) and nothing
# else, so initgroups() -- which passes the account's real supplementary list --
# is killed by the filter. Clearing is equivalent here anyway: the only group
# that grants anything is the primary gid, which --regid already sets.
# Point HOME and XDG_* at a writable tree owned by snap_daemon.
# sudo/GHA often leave XDG_CONFIG_HOME=/home/<invoker>/.config; yarn then
# tries to create /home/runner/.config/yarn and dies with EACCES after we
# drop privileges. Override every path yarn/npm/node consult for config/cache.
export_daemon_runtime_dirs() {
  export HOME="$SNAP_COMMON/bench"
  export XDG_CONFIG_HOME="$HOME/.config"
  export XDG_CACHE_HOME="$HOME/.cache"
  export XDG_DATA_HOME="$HOME/.local/share"
  export YARN_CACHE_FOLDER="$HOME/.cache/yarn"
  export npm_config_cache="$HOME/.cache/npm"
  mkdir -p "$XDG_CONFIG_HOME" "$XDG_CACHE_HOME" "$XDG_DATA_HOME" \
           "$YARN_CACHE_FOLDER" "$npm_config_cache" 2>/dev/null || true
  chown -R "$DAEMON_USER:$DAEMON_USER" \
    "$HOME/.config" "$HOME/.cache" "$HOME/.local" 2>/dev/null || true
}

run_as_daemon() {
  if [ "$(id -u)" = "0" ]; then
    export_daemon_runtime_dirs
    exec setpriv --reuid="$DAEMON_USER" --regid="$DAEMON_USER" --clear-groups "$@"
  fi
  exec "$@"
}

# Like run_as_daemon, but enters $1 as the working directory before exec'ing
# the rest. The cd MUST happen after setpriv: bootstrap_common's share_mode
# leaves the bench at 0770 owned by snap_daemon, and root inside a strict snap
# has no CAP_DAC_OVERRIDE, so `cd "$LIVE_BENCH"` as root fails with
# "Permission denied". Measured on the appliance: the first service to start
# after a bench switch wins (cd while still 0775, then strips other bits);
# worker / socketio / watch then die in a restart loop. web-wrapper already
# avoided this by passing gunicorn --chdir after setpriv; this helper is the
# same idea for every other service and for `sudo vybench.bench`.
run_as_daemon_in() {
  local dir="$1"; shift
  if [ "$(id -u)" = "0" ]; then
    export_daemon_runtime_dirs
    exec setpriv --reuid="$DAEMON_USER" --regid="$DAEMON_USER" --clear-groups \
      bash -c 'cd "$1" || exit 1; shift; exec "$@"' bash "$dir" "$@"
  fi
  cd "$dir" || exit 1
  exec "$@"
}

# Re-exec the calling script itself as snap_daemon, so that every command after
# this point runs unprivileged. Use this instead of run_as_daemon when a wrapper
# has to run several commands unprivileged (e.g. mariadb-install-db then
# mariadbd). Call it AFTER bootstrap_common, which needs root to chown.
# Returns normally when already unprivileged, so the script simply continues.
reexec_as_daemon() {
  if [ "$(id -u)" = "0" ]; then
    export_daemon_runtime_dirs
    exec setpriv --reuid="$DAEMON_USER" --regid="$DAEMON_USER" --clear-groups "$@"
  fi
}

# Current install mode: "production" (default) or "developer".
# Set with: snap set vybench mode=developer
get_mode() {
  local m
  m=$(snapctl get mode 2>/dev/null) || m=""
  case "$m" in
    developer|dev) echo developer ;;
    *)             echo production ;;
  esac
}

# Read a value out of common_site_config.json (empty string if absent).
site_conf() {
  "$BENCH_PY" - "$1" <<'PYEOF' 2>/dev/null || true
import json, os, sys
bench = os.environ.get("LIVE_BENCH") or os.path.join(os.environ["SNAP_COMMON"], "bench")
path = os.path.join(bench, "sites", "common_site_config.json")
try:
    with open(path) as fh:
        print(json.load(fh).get(sys.argv[1], "") or "")
except Exception:
    print("")
PYEOF
}
