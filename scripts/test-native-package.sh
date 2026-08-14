#!/usr/bin/env bash
# Install the built .deb/.rpm in a CLEAN container of its target distribution and
# check that it actually runs.
#
# This is not optional polish. Every defect listed in docs/native-packaging-plan.md
# was found by RUNNING a package, not by reading one, and the package this
# replaces installed perfectly while being completely non-functional. A build
# that is never installed proves nothing.
#
# It must be a clean container and not the build host: absolute build-time paths
# are invisible on the machine that produced them, because those paths still
# exist there.
#
#   scripts/test-native-package.sh --distro ubuntu:24.04
set -euo pipefail

DISTRO="ubuntu:24.04"
ENGINE=""
PKG=""
SITE="test.localhost"
ADMIN_PW="admin"
KEEP=0

while [ $# -gt 0 ]; do
  case "$1" in
    --distro)  DISTRO="$2"; shift 2 ;;
    --engine)  ENGINE="$2"; shift 2 ;;
    --package) PKG="$2"; shift 2 ;;
    --keep)    KEEP=1; shift ;;
    -h|--help) sed -n '2,16p' "$0"; exit 0 ;;
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

case "$DISTRO" in
  ubuntu:*|debian:*) FAMILY=debian; EXT=deb ;;
  *)                 FAMILY=rhel;   EXT=rpm ;;
esac

if [ -z "$PKG" ]; then
  found=()
  for p in "$ROOT_DIR"/dist/*."$EXT"; do [ -f "$p" ] && found+=("$p"); done
  [ ${#found[@]} -ge 1 ] || { echo "FATAL: no .$EXT in dist/" >&2; exit 1; }
  PKG="${found[$(( ${#found[@]} - 1 ))]}"
fi
echo "==> testing $(basename "$PKG") on $DISTRO"

SLUG="$(echo "$DISTRO" | tr ':/' '--')"
IMAGE="frappista-test:$SLUG"
NAME="frappista-test-$SLUG-$$"

# ---------------------------------------------------------------------------
# A systemd-capable image
# ---------------------------------------------------------------------------
# The units are the deliverable, so the test has to boot systemd -- checking the
# binaries by hand would pass on exactly the package that fails at boot. Debian
# and Ubuntu base images ship no init at all, so one gets installed here.
BUILD_CTX="$(mktemp -d "${TMPDIR:-/tmp}/frappista-testimg.XXXXXX")"
trap 'rm -rf "$BUILD_CTX"' EXIT

if [ "$FAMILY" = "debian" ]; then
  cat > "$BUILD_CTX/Containerfile" <<EOF
FROM $DISTRO
ENV DEBIAN_FRONTEND=noninteractive container=docker
RUN apt-get update -qq && \
    apt-get install -y --no-install-recommends systemd systemd-sysv dbus curl procps && \
    apt-get clean && rm -rf /var/lib/apt/lists/*
# systemd in a container should not try to manage the host's hardware, network
# or console. Leaving these enabled makes the boot hang instead of fail.
RUN find /etc/systemd/system /lib/systemd/system \\
      -path '*.wants/*' \\
      \\( -name '*udev*' -o -name '*getty*' -o -name '*console*' \\) \\
      -exec rm -f {} + || true
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
EOF
else
  cat > "$BUILD_CTX/Containerfile" <<EOF
FROM $DISTRO
ENV container=docker
RUN (dnf -y install systemd procps-ng curl || yum -y install systemd procps-ng curl) && \
    (dnf clean all || yum clean all)
RUN find /etc/systemd/system /usr/lib/systemd/system \\
      -path '*.wants/*' \\
      \\( -name '*udev*' -o -name '*getty*' -o -name '*console*' \\) \\
      -exec rm -f {} + || true
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
EOF
fi

echo "==> building systemd test image"
"$ENGINE" build -t "$IMAGE" -f "$BUILD_CTX/Containerfile" "$BUILD_CTX"

cleanup() {
  if [ "$KEEP" = "1" ]; then
    echo "==> container '$NAME' left running (--keep); remove with: $ENGINE rm -f $NAME"
  else
    "$ENGINE" rm -f "$NAME" >/dev/null 2>&1 || true
  fi
  rm -rf "$BUILD_CTX"
}
trap cleanup EXIT

echo "==> booting systemd container"
"$ENGINE" run -d --name "$NAME" \
  --privileged --cgroupns=host \
  -v /sys/fs/cgroup:/sys/fs/cgroup:rw \
  --tmpfs /run --tmpfs /run/lock --tmpfs /tmp \
  -v "$ROOT_DIR/dist":/pkgs:ro,z \
  "$IMAGE"

x() { "$ENGINE" exec "$NAME" bash -lc "$1"; }

# "degraded" is the normal outcome in a container -- a handful of units that
# expect real hardware always fail -- so it counts as booted.
STATE=""
for _ in $(seq 1 60); do
  STATE="$(x 'systemctl is-system-running' 2>/dev/null || true)"
  case "$STATE" in *running*|*degraded*) break ;; esac
  sleep 2
done
echo "==> systemd state: ${STATE:-unknown}"
[ -n "$STATE" ] || { echo "FATAL: systemd never came up in the test container" >&2; exit 1; }

# ---------------------------------------------------------------------------
# Install
# ---------------------------------------------------------------------------
echo "==> installing the package"
if [ "$FAMILY" = "debian" ]; then
  x "apt-get update -qq && apt-get install -y /pkgs/$(basename "$PKG")"
else
  x "(dnf -y install /pkgs/$(basename "$PKG") || yum -y install /pkgs/$(basename "$PKG"))"
fi

FAILURES=0
# Exactly two arguments: the label and ONE command string. Not "$*" -- several
# checks below are multi-line heredocs, and joining the arguments would collapse
# their newlines into spaces and silently break them.
check() {
  if x "$2" >/dev/null 2>&1; then
    printf '  PASS  %s\n' "$1"
  else
    printf '  FAIL  %s\n' "$1"
    FAILURES=$((FAILURES + 1))
  fi
}

# The container has no sudo on minimal images; runuser is util-linux and always
# present. HOME must be set explicitly or bench scatters cache into /root.
AS_FRAPPE='runuser -u frappe -- env HOME=/opt/frappe-bench'

echo "==> pre-setup checks"

# The defect this whole rewrite exists to fix: the old .deb created
# /opt/frappe-bench and never put anything in it, so every unit's ExecStart
# pointed at a file that did not exist -- and `dpkg -i` still reported success.
check "payload ships a real bench"        'test -x /opt/frappe-bench/env/bin/bench'
check "payload ships node"                'test -x /opt/frappe-bench/node/bin/node'
check "venv interpreter works"            '/opt/frappe-bench/env/bin/python -c "import ssl, sqlite3, lzma"'
check "frappe imports"                    'cd /opt/frappe-bench/sites && /opt/frappe-bench/env/bin/python -c "import frappe"'
# is_bench_directory() needs all five; config/pids is the one that goes missing,
# and its absence makes `bench worker` fail with "No such command 'worker'".
check "bench layout complete"             'for d in apps sites config config/pids logs; do test -d /opt/frappe-bench/$d || exit 1; done'
check "sites/apps.txt present"            'test -s /opt/frappe-bench/sites/apps.txt'
check "frappe is first in apps.txt"       'head -1 /opt/frappe-bench/sites/apps.txt | grep -qx frappe'
# Invisible on the build host, fatal here: this is exactly how the snap's
# dangling assets/frappe symlink survived until it was installed elsewhere.
check "no dangling symlinks in sites/env" '! find /opt/frappe-bench/sites /opt/frappe-bench/env/bin -xtype l | grep -q .'
check "frappe service account exists"     'getent passwd frappe'
# `! -type l` matters: a symlink is always lrwxrwxrwx, chmod cannot change that,
# and find -perm uses lstat -- so the venv's own env/lib64 -> lib link reads as
# world-writable and fails a check that is otherwise correct.
check "bench is not world-writable" \
  '! find /opt/frappe-bench -maxdepth 2 -perm -0002 ! -type l | grep -q .'

# A unit that still contains @MARIADB_SVC@ or points at a binary that was never
# shipped fails at boot with a message nobody reads. Check both directly rather
# than relying on systemd-analyze, whose exit status is not reliable across the
# systemd versions in this matrix.
check "units carry no placeholders" '
  ! grep -l "@[A-Z_]*@" /lib/systemd/system/frappe*.service \
      /lib/systemd/system/frappista.target \
      /usr/lib/systemd/system/frappe*.service \
      /usr/lib/systemd/system/frappista.target 2>/dev/null | grep -q .
'
check "every ExecStart binary exists" '
  set -e
  for f in /lib/systemd/system/frappe*.service /usr/lib/systemd/system/frappe*.service; do
    [ -f "$f" ] || continue
    bin=$(sed -n "s/^ExecStart=\([^ ]*\).*/\1/p" "$f" | head -1)
    [ -x "$bin" ] || { echo "missing: $bin (from $f)"; exit 1; }
  done
'
# The "No such command '"'"'worker'"'"'" failure -- caused by an incomplete bench
# layout -- shows up here rather than as a mysteriously dead unit.
check "bench subcommands load" \
  "cd /opt/frappe-bench && $AS_FRAPPE /opt/frappe-bench/env/bin/bench worker --help"

# ---------------------------------------------------------------------------
# Setup
# ---------------------------------------------------------------------------
echo "==> frappista-setup"
# Non-interactive by construction: if the database administrator account were
# not provisioned first, `bench new-site` would call getpass() and die with
# EOFError right here.
x "frappista-setup --site $SITE --admin-password $ADMIN_PW"

echo "==> post-setup checks"

UNITS="frappe-web frappe-scheduler frappe-socketio frappe-worker@default frappe-worker@short frappe-worker@long"
for u in $UNITS; do
  check "unit active: $u" "systemctl is-active --quiet $u.service"
done

# Every unit declares User=frappe. If any of them is running as root, bench's
# change_uid() is in play and the ownership model is not what it claims to be.
check "no frappe process runs as root" \
  '! ps -eo user:32,args | grep -E "(gunicorn|bench worker|bench schedule|socketio.js)" | grep -v grep | grep -q "^root"'

check "site directory created"            "test -d /opt/frappe-bench/sites/$SITE"

# Every app the payload ships must actually be installed INTO the site.
# `bench new-site` installs frappe alone, so a package advertising ERPNext can
# otherwise hand the operator a bare Frappe desk with no error anywhere -- the
# app is on disk, just not in the site.
check "all shipped apps installed in the site" "
  installed=\$(cd /opt/frappe-bench && $AS_FRAPPE /opt/frappe-bench/env/bin/bench --site $SITE list-apps 2>/dev/null)
  while read -r app; do
    [ -n \"\$app\" ] || continue
    echo \"\$installed\" | grep -qw \"\$app\" || { echo \"missing from site: \$app\"; exit 1; }
  done < /opt/frappe-bench/sites/apps.txt
"
check "site scheduler enabled" \
  "cd /opt/frappe-bench && $AS_FRAPPE /opt/frappe-bench/env/bin/bench --site $SITE scheduler status | grep -qi enabled"

# HTTP through nginx, which is the only thing serving static files -- gunicorn
# runs frappe.app:application, which serves none.
check "nginx serves the desk (200)" \
  "test \"\$(curl -s -o /dev/null -w '%{http_code}' -H 'Host: $SITE' http://127.0.0.1/login)\" = 200"

check "static assets resolve (200)" \
  "asset=\$(cd /opt/frappe-bench/sites && ls assets/frappe/dist/css/*.css 2>/dev/null | head -1); \
   test -n \"\$asset\" && \
   test \"\$(curl -s -o /dev/null -w '%{http_code}' -H 'Host: $SITE' http://127.0.0.1/\$asset)\" = 200"

check "socketio port listening"           'curl -s -o /dev/null http://127.0.0.1:9000/socket.io/?EIO=4\&transport=polling'

# ---------------------------------------------------------------------------
# Background queues
# ---------------------------------------------------------------------------
# The old package ran one worker on the `default` queue. Anything enqueued to
# short or long was accepted by redis and never executed -- no error, no log
# line, the work simply never happened. Enqueue to each queue and require it to
# drain.
echo "==> queue drain test"
for q in default short long; do
  out="$(x "cd /opt/frappe-bench && $AS_FRAPPE /opt/frappe-bench/env/bin/bench --site $SITE execute \
     frappe.utils.background_jobs.enqueue \
     --kwargs \"{'method':'frappe.utils.now','queue':'$q'}\"" 2>&1)" && rc=0 || rc=$?

  # `bench execute` exits non-zero here even on success: enqueue() returns an
  # rq.job.Job, and bench tries to JSON-serialise the return value, which raises
  # TypeError *after* the job is safely on the queue. The Job repr in the output
  # is the proof it worked -- treat that as success rather than reporting a
  # failure that did not happen.
  if [ "$rc" -eq 0 ] || printf '%s' "$out" | grep -q "Job('"; then
    printf '  OK    enqueued to %s\n' "$q"
  else
    echo "  WARN  could not enqueue to '$q':" >&2
    printf '        %s\n' "$out" | tail -5 >&2
  fi
done

# Assert jobs were EXECUTED, not merely that the queue is empty. RQ deletes a
# queue's list key as soon as it drains, so "key exists with length 0" is a state
# a working system never reaches -- checking for it fails on success. RQ's
# finished-job registry is the durable record of work actually completed.
check "every queue executed a job" '
  /opt/frappe-bench/env/bin/python - <<PY
import json, sys, time, redis
cfg = json.load(open("/opt/frappe-bench/sites/common_site_config.json"))
r = redis.from_url(cfg["redis_queue"])
want = {"default", "short", "long"}
# frappe namespaces queues as "<bench_id>:<qtype>", so match on the last segment.
done, fin = set(), []
deadline = time.time() + 120
while time.time() < deadline:
    fin = [k.decode() for k in r.keys("rq:finished:*")]
    done = {k.rsplit(":", 1)[-1] for k in fin if r.zcard(k) > 0} & want
    if done == want:
        print("executed on every queue:", sorted(done))
        sys.exit(0)
    time.sleep(3)
print("completed only on:", sorted(done), "expected:", sorted(want))
print("finished registries:", fin)
print("still queued:", {k.decode(): r.llen(k) for k in r.keys("rq:queue:*")})
sys.exit(1)
PY
'

# ---------------------------------------------------------------------------
echo
if [ "$FAILURES" -eq 0 ]; then
  echo "==> all checks passed for $(basename "$PKG") on $DISTRO"
  exit 0
fi

echo "==> $FAILURES check(s) FAILED on $DISTRO"
echo "--- journal (last 60 lines per failing unit) ---"
for u in $UNITS; do
  x "systemctl is-active --quiet $u.service" >/dev/null 2>&1 || \
    x "journalctl -u $u.service -n 60 --no-pager" || true
done
exit 1
