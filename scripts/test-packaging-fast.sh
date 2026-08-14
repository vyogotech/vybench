#!/usr/bin/env bash
# Fast packaging tests -- roughly one minute, no bench build.
#
# There are two tiers of test here, and the split matters:
#
#   FAST (this script)  synthetic payload, no network, no bench init. Catches
#                       everything about how the package is ASSEMBLED: missing
#                       files, unsubstituted placeholders, wrong service names
#                       per distro family, a spec that will not parse, template
#                       drift, shell bugs.
#
#   SLOW (test-native-package.sh)  builds a real payload, installs the package in
#                       a clean systemd container, asserts the services run.
#                       Catches everything about how the package BEHAVES.
#
# The fast tier exists because the slow one costs ~40 minutes per distribution.
# Most defects found while building this project were assembly defects, and every
# one of them would have been caught here:
#
#   - a payload missing env/bin/gunicorn                (the original empty .deb)
#   - @MARIADB_SVC@ left unsubstituted in a unit
#   - redis-server.service vs redis.service swapped
#   - %{_unitdir} undefined on RHEL, "File must begin with /"
#   - a bogus %changelog date
#   - the nginx vhost still pointing at the container's document root
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/vybench-fast.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

PASS=0; FAIL=0
ok()   { printf '  PASS  %s\n' "$1"; PASS=$((PASS+1)); }
bad()  { printf '  FAIL  %s\n' "$1"; [ -n "${2:-}" ] && printf '        %s\n' "$2"; FAIL=$((FAIL+1)); }
try()  { if eval "$2" >/dev/null 2>&1; then ok "$1"; else bad "$1"; fi; }

echo "==> static checks"

for f in "$ROOT"/scripts/*.sh "$ROOT"/scripts/lib/*.sh "$ROOT"/scripts/templates/vybench-setup \
         "$ROOT"/snap/local/bin/*; do
  [ -f "$f" ] || continue
  case "$f" in *.json|*.template) continue ;; esac
  if bash -n "$f" 2>/dev/null; then :; else bad "shell syntax: ${f#"$ROOT"/}"; fi
done
ok "shell syntax (all scripts)"

if command -v shellcheck >/dev/null 2>&1; then
  if shellcheck -S warning "$ROOT"/scripts/*.sh "$ROOT"/scripts/lib/*.sh \
       "$ROOT"/scripts/templates/vybench-setup 2>/dev/null; then
    ok "shellcheck (warning level)"
  else
    bad "shellcheck (warning level)"
  fi
else
  echo "  SKIP  shellcheck (not installed)"
fi

# PyYAML is not in the standard library, so treat it like shellcheck and rpmspec:
# skip loudly rather than fail, and install it in CI where the check must run.
# A test that fails because a tool is missing trains people to ignore failures.
if python3 -c 'import yaml' 2>/dev/null; then
  try "snapcraft.yaml is valid YAML" \
      "python3 -c \"import yaml;yaml.safe_load(open('$ROOT/snap/snapcraft.yaml'))\""
else
  echo "  SKIP  snapcraft.yaml YAML check (PyYAML not installed)"
fi
try "common_site_config.json is valid JSON" \
    "python3 -c \"import json;json.load(open('$ROOT/scripts/templates/common_site_config.json'))\""

# frappe resolves redis and the database through this file; a typo here surfaces
# only at runtime, as a site that cannot connect to anything.
try "common_site_config has the keys frappe needs" \
    "python3 -c \"
import json,sys
c=json.load(open('$ROOT/scripts/templates/common_site_config.json'))
need={'db_host','db_port','redis_cache','redis_queue','redis_socketio','webserver_port','socketio_port'}
missing=need-set(c)
sys.exit(1) if missing else None\""

try "nginx template has not drifted" "$ROOT/scripts/check-nginx-template-drift.sh"

echo "==> synthetic payload"
# Minimal tree containing exactly what the stager asserts, and nothing else.
SP="$WORK/payload"
mkdir -p "$SP/opt/frappe-bench"/{env/bin,node/bin,sites,config/pids,logs,apps} \
         "$SP/usr/lib/vybench/python/bin"
for b in opt/frappe-bench/env/bin/bench opt/frappe-bench/env/bin/gunicorn \
         opt/frappe-bench/node/bin/node usr/lib/vybench/python/bin/python3.14; do
  printf '#!/bin/sh\nexit 0\n' > "$SP/$b"; chmod +x "$SP/$b"
done
printf 'frappe\n' > "$SP/opt/frappe-bench/sites/apps.txt"
tar -C "$SP" -czf "$WORK/payload.tar.gz" opt/frappe-bench usr/lib/vybench
ok "synthetic payload built"

# shellcheck source=scripts/lib/stage-native-root.sh
. "$ROOT/scripts/lib/stage-native-root.sh"

for family in debian rhel; do
  echo "==> staging ($family)"
  DEST="$WORK/root-$family"
  if stage_native_root "$DEST" "$family" "$WORK/payload.tar.gz" >/dev/null 2>&1; then
    ok "stages for $family"
  else
    bad "stages for $family"; continue
  fi

  case "$family" in
    debian) UNIT_DIR="$DEST/lib/systemd/system";     WANT_REDIS=redis-server.service
            CNF="$DEST/etc/mysql/mariadb.conf.d/10-frappe.cnf" ;;
    rhel)   UNIT_DIR="$DEST/usr/lib/systemd/system"; WANT_REDIS=redis.service
            CNF="$DEST/etc/my.cnf.d/10-frappe.cnf" ;;
  esac

  # A leftover @PLACEHOLDER@ makes systemd refuse the unit at boot, long after
  # the package looked fine.
  if grep -rl '@[A-Z_]*@' "$UNIT_DIR" 2>/dev/null | grep -q .; then
    bad "$family: units carry no placeholders" "$(grep -rn '@[A-Z_]*@' "$UNIT_DIR" | head -2)"
  else
    ok "$family: units carry no placeholders"
  fi

  # The one thing that genuinely differs between the families. Getting it
  # backwards yields units that never start, with a dependency error nobody reads.
  if grep -q "$WANT_REDIS" "$UNIT_DIR/frappe-web.service" 2>/dev/null; then
    ok "$family: redis unit is $WANT_REDIS"
  else
    bad "$family: redis unit is $WANT_REDIS" "$(grep -h 'redis' "$UNIT_DIR/frappe-web.service" 2>/dev/null | head -1)"
  fi

  MISSING=""
  for f in vybench.target frappe-web.service 'frappe-worker@.service' \
           frappe-scheduler.service frappe-socketio.service; do
    [ -f "$UNIT_DIR/$f" ] || MISSING="$MISSING $f"
  done
  [ -z "$MISSING" ] && ok "$family: all five units present" || bad "$family: all five units present" "missing:$MISSING"

  # Every ExecStart must point at something the payload actually ships -- this is
  # the check that would have caught the original empty /opt/frappe-bench.
  BADBIN=""
  for u in "$UNIT_DIR"/frappe-*.service; do
    bin=$(sed -n 's/^ExecStart=\([^ ]*\).*/\1/p' "$u" | head -1)
    [ -e "$DEST$bin" ] || BADBIN="$BADBIN $(basename "$u"):$bin"
  done
  [ -z "$BADBIN" ] && ok "$family: every ExecStart binary is shipped" \
                   || bad "$family: every ExecStart binary is shipped" "$BADBIN"

  try "$family: mariadb drop-in installed" "test -f '$CNF'"
  try "$family: vybench-setup installed" "test -x '$DEST/usr/bin/vybench-setup'"
  try "$family: bundled python shipped" "test -x '$DEST/usr/lib/vybench/python/bin/python3.14'"

  TPL="$DEST/etc/vybench/nginx/frappe.conf.template"
  try "$family: vhost document root is templated" "grep -q 'root \${BENCH_SITES};' '$TPL'"
  try "$family: vhost listen port is templated"   "grep -q 'listen \${NGINX_PORT};' '$TPL'"
  try "$family: vhost drops the container path"   "! grep -q '/home/frappe/frappe-bench' '$TPL'"
done

echo "==> negative test"
# The stager must REFUSE an incomplete payload. Without this the whole guard
# could rot into a no-op and nothing would notice.
BAD="$WORK/bad"; mkdir -p "$BAD/opt/frappe-bench/env/bin"
printf '#!/bin/sh\n' > "$BAD/opt/frappe-bench/env/bin/bench"; chmod +x "$BAD/opt/frappe-bench/env/bin/bench"
tar -C "$BAD" -czf "$WORK/bad.tar.gz" opt/frappe-bench
if stage_native_root "$WORK/root-bad" debian "$WORK/bad.tar.gz" >/dev/null 2>&1; then
  bad "incomplete payload is rejected" "the stager accepted a payload with no gunicorn, node or python"
else
  ok "incomplete payload is rejected"
fi

echo "==> rpm spec"
if command -v rpmspec >/dev/null 2>&1; then
  # Native arch, not a hardcoded one: rpmspec rejects a BuildArch it cannot
  # build ("No compatible architectures found for build"), so a hardcoded x86_64
  # makes this test fail on every arm64 machine for a reason unrelated to the spec.
  if rpmspec --parse "$ROOT/packaging/vybench.spec" \
       --define "vybench_version 0.0.0" --define "vybench_arch $(uname -m)" \
       --define "python_xy 3.14" --define "distro_tag test" \
       --define "frappe_branch version-16" --define "runtime_requires glibc" \
       >/dev/null 2>&1; then
    ok "vybench.spec parses"
  else
    bad "vybench.spec parses" \
        "$(rpmspec --parse "$ROOT/packaging/vybench.spec" \
             --define "vybench_version 0.0.0" --define "vybench_arch $(uname -m)" \
             --define "python_xy 3.14" --define "distro_tag test" \
             --define "frappe_branch version-16" --define "runtime_requires glibc" 2>&1 | tail -3)"
  fi
else
  echo "  SKIP  rpm spec parse (rpmspec not installed)"
fi

echo
echo "==> $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
