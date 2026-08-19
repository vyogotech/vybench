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

# $SNAP is revision-specific (/snap/vybench/x11). Anything persisted into
# $SNAP_COMMON and expected to survive a refresh must point at the revision
# -stable `current` symlink instead, or it dangles after the next upgrade.
SNAP_STABLE=/snap/vybench/current

# Where the live apps/ and env/ actually are.
#
# Production keeps them as symlinks into the read-only squashfs: immutable, and
# `snap revert` rolls them back atomically. A developer install materialises them
# into $SNAP_COMMON (see materialise_bench) so that get-app / new-app / source
# edits / pip install / bench build all work like a normal bench. Detect which
# layout is in play rather than assuming, so both modes use one code path.
if [ -d "$SNAP_COMMON/bench/env" ] && [ ! -L "$SNAP_COMMON/bench/env" ]; then
  BENCH_ROOT="$SNAP_COMMON/bench"          # materialised, writable
else
  BENCH_ROOT="$SNAP/opt/frappe-bench"      # squashfs, read-only
fi
BENCH_PY="$BENCH_ROOT/env/bin/python3.14"
BENCH_CLI="$BENCH_ROOT/env/bin/bench"

export PATH="$SNAP/usr/sbin:$SNAP/usr/bin:$SNAP/sbin:$SNAP/bin:$BENCH_ROOT/env/bin:$PATH"
export LD_LIBRARY_PATH="$SNAP/usr/lib/x86_64-linux-gnu:$SNAP/usr/lib/aarch64-linux-gnu:$SNAP/usr/lib:$SNAP/lib/x86_64-linux-gnu:$SNAP/lib/aarch64-linux-gnu:$SNAP/lib:$LD_LIBRARY_PATH"
export PYTHONPATH="$BENCH_ROOT/apps/frappe:$BENCH_ROOT/env/lib/python3.14/site-packages"
# The socket lives OUTSIDE the data directory on purpose. The datadir holds the
# actual database files and stays private to snap_daemon (0770); the socket
# directory is world-traversable so any local user can connect without being
# added to a group. Connecting is not authorisation -- MariaDB still demands the
# generated root password -- so an open socket costs nothing.
export MYSQL_UNIX_PORT="$SNAP_COMMON/run/mysql.sock"

# The bench baked into $SNAP has its own sites/common_site_config.json left over
# from `bench init`, carrying bench's DEFAULT ports (redis_queue 11000,
# redis_cache 13000) and frappe_user=frappe. frappe's node code resolves that
# file relative to __dirname, so socketio would read the build-time config and
# dial redis on 11000. Point every process at the live writable bench instead.
export FRAPPE_BENCH_ROOT="$SNAP_COMMON/bench"

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

# Datastore directories. These belong to snap_daemon in BOTH modes, because
# mariadbd and redis always run as that account. The bench tree is deliberately
# not touched here -- see bootstrap_bench.
bootstrap_datastores() {
  mkdir -p "$SNAP_COMMON/mariadb" "$SNAP_COMMON/redis" "$SNAP_COMMON/run" 2>/dev/null || true

  if [ "$(id -u)" = "0" ]; then
    chown "$DAEMON_USER:$DAEMON_USER" \
      "$SNAP_COMMON/mariadb" "$SNAP_COMMON/redis" "$SNAP_COMMON/run" 2>/dev/null || true
  fi

  # Database files stay private...
  chmod u+rwx,g+rwxs,o-rwx "$SNAP_COMMON/mariadb" "$SNAP_COMMON/redis" 2>/dev/null || true
  # ...but the socket directory is traversable, so `bench` works for any local
  # user with no group membership. Auth still gates actual database access.
  chmod 0755 "$SNAP_COMMON/run" 2>/dev/null || true
}

# Create the writable runtime layout and hand it to snap_daemon.
bootstrap_common() {
  bootstrap_datastores
  # bench's is_bench_directory() requires ALL of paths_in_bench to exist:
  #   ("apps", "sites", "config", "logs", "config/pids")
  # Miss any one -- config/pids is the easy one to forget -- and bench decides it
  # is not in a bench, silently skips loading frappe's subcommands, and
  # `bench worker` dies with "No such command 'worker'".
  mkdir -p "$SNAP_COMMON/bench/logs" "$SNAP_COMMON/bench/config/pids" \
           "$SNAP_COMMON/bench/sites" 2>/dev/null || true

  # Link apps/ and env/ at the squashfs -- but ONLY while they are still links.
  # Once materialise_bench has replaced them with real directories, `ln -sf` would
  # create the link *inside* the directory (bench/env/env -> ...), quietly
  # corrupting a developer bench every time a service restarts.
  for tree in env apps; do
    if [ ! -e "$SNAP_COMMON/bench/$tree" ] || [ -L "$SNAP_COMMON/bench/$tree" ]; then
      as_daemon ln -sfn "$SNAP_STABLE/opt/frappe-bench/$tree" \
              "$SNAP_COMMON/bench/$tree" 2>/dev/null || true
    fi
  done

  # Seed the writable sites/ directory from the bench baked into the snap. bench
  # discovers installed apps through sites/apps.txt; without it even
  # `bench worker` fails with "No such command 'worker'", because frappe's
  # subcommands are never loaded. assets/ is symlinked rather than copied: it is
  # large, read-only, and version-locked to this squashfs revision.
  for seed in apps.txt apps.json; do
    if [ ! -f "$SNAP_COMMON/bench/sites/$seed" ] && \
       [ -f "$SNAP/opt/frappe-bench/sites/$seed" ]; then
      as_daemon cp "$SNAP/opt/frappe-bench/sites/$seed" "$SNAP_COMMON/bench/sites/$seed" 2>/dev/null || true
    fi
  done
  # Same guard as apps/env: leave a materialised (real) assets tree alone.
  if [ ! -e "$SNAP_COMMON/bench/sites/assets" ] || [ -L "$SNAP_COMMON/bench/sites/assets" ]; then
    as_daemon ln -sfn "$SNAP_STABLE/opt/frappe-bench/sites/assets" \
            "$SNAP_COMMON/bench/sites/assets" 2>/dev/null || true
  fi

  # Seed from the static config shipped in the snap rather than generating JSON
  # here. The same file is baked over the bench's build-time config, so both
  # copies agree by construction.
  if [ ! -f "$SNAP_COMMON/bench/sites/common_site_config.json" ]; then
    as_daemon cp "$SNAP/config/common_site_config.json" \
       "$SNAP_COMMON/bench/sites/common_site_config.json" 2>/dev/null || true
  fi

  # Shared-write surface between the snap_daemon services and the CLI user.
  # Deliberately scoped: apps/ and env/ are excluded because on a materialised
  # developer bench they are ~1GB and recursing them on every one of six daemon
  # starts is seconds of pointless I/O. materialise_bench fixes their modes once.
  local shared="$SNAP_COMMON/bench/sites $SNAP_COMMON/bench/config \
                $SNAP_COMMON/bench/logs"

  # Only claim the bench for snap_daemon when the managed app tier actually owns
  # it -- i.e. production. In developer mode those daemons are disabled and never
  # touch this tree, so leaving it owned by whoever ran `bench` means the
  # developer needs neither a group nor sudo, and git sees a repo it owns.
  if [ "$(id -u)" = "0" ] && [ "$(get_mode)" = "production" ]; then
    chown "$DAEMON_USER:$DAEMON_USER" "$SNAP_COMMON/bench" 2>/dev/null || true
    # shellcheck disable=SC2086
    chown -R "$DAEMON_USER:$DAEMON_USER" $shared 2>/dev/null || true
  fi

  # ug+rwX, not 777: owner and the snap_daemon group get access, everyone else
  # gets nothing. g+s on directories makes new files inherit the group, so files
  # created by the CLI user stay writable by the daemons and vice versa -- without
  # it the sharing silently decays as soon as either side writes something new.
  chmod u+rwx,g+rwxs,o-rwx "$SNAP_COMMON/bench" 2>/dev/null || true
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
  local B="$SNAP_COMMON/bench"
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

  # The venv's interpreter link is relative (../../../../usr/bin/python3.14),
  # which counts four levels from $SNAP/opt/frappe-bench/env/bin but lands on
  # /var/snap/vybench/usr/bin from the copied location -- a dangling link that
  # breaks every bench command. Re-point it absolutely at the bundled interpreter.
  # Also rewrite build-time shebangs (#!/build/vybench/...) to #!/usr/bin/env python3.
  if [ -d "$B/env/bin" ]; then
    as_daemon ln -sfn "$SNAP_STABLE/usr/bin/python3.14" "$B/env/bin/python3.14"
    as_daemon ln -sfn python3.14 "$B/env/bin/python3"
    as_daemon ln -sfn python3.14 "$B/env/bin/python"
    as_daemon find "$B/env/bin" -type f -exec sed -i '1s|^#!/build/.*python.*|#!/usr/bin/env python3|' {} + 2>/dev/null || true
    [ -e "$B/env/bin/python3.14" ] || echo "WARNING: materialised venv python is dangling" >&2
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

  local probe="$SNAP_COMMON/bench/sites"
  [ -d "$probe" ] || return 0            # nothing bootstrapped yet; nothing to check
  [ -w "$probe" ] && return 0            # already writable -- say nothing

  cat >&2 <<EOF
vybench: the bench at $SNAP_COMMON/bench is owned by '$DAEMON_USER' and is not
writable by $(id -un).

This install is in production mode, where the services own the bench. Either:

  sudo vybench.bench $*
      run the command as the service account (no setup needed), or

  sudo usermod -aG $DAEMON_USER $(id -un)   # then re-login, or: newgrp $DAEMON_USER
      grant yourself permanent access, like docker's post-install step.

A developer install (snap set vybench mode=developer) needs neither.
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
run_as_daemon() {
  if [ "$(id -u)" = "0" ]; then
    export HOME="$SNAP_COMMON/bench"
    exec setpriv --reuid="$DAEMON_USER" --regid="$DAEMON_USER" --clear-groups "$@"
  fi
  exec "$@"
}

# Re-exec the calling script itself as snap_daemon, so that every command after
# this point runs unprivileged. Use this instead of run_as_daemon when a wrapper
# has to run several commands unprivileged (e.g. mariadb-install-db then
# mariadbd). Call it AFTER bootstrap_common, which needs root to chown.
# Returns normally when already unprivileged, so the script simply continues.
reexec_as_daemon() {
  if [ "$(id -u)" = "0" ]; then
    export HOME="$SNAP_COMMON/bench"
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
path = os.path.join(os.environ["SNAP_COMMON"], "bench", "sites", "common_site_config.json")
try:
    with open(path) as fh:
        print(json.load(fh).get(sys.argv[1], "") or "")
except Exception:
    print("")
PYEOF
}
