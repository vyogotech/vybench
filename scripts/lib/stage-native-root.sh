#!/usr/bin/env bash
# Shared staging logic for the .deb and .rpm packagers.
#
# Both packages ship exactly the same filesystem; only the metadata, the
# dependency syntax and the maintainer-script dialect differ. Keeping the layout
# in one place is the difference between two packages that agree and two
# packages that drift.
#
# Sourced, not executed. Provides:
#   stage_native_root <destdir> <family:debian|rhel> <payload.tar.gz>
#
# shellcheck shell=bash

BENCH_DIR=/opt/frappe-bench
# Where build-bench-payload.sh installs the bundled CPython 3.14. Kept in sync
# with that script; both packagers assert it is present.
PYTHON_HOME=/usr/lib/frappium/python

stage_native_root() {
  local dest="$1" family="$2" payload="$3"
  local script_dir root_dir tpl
  script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  root_dir="$(cd "$script_dir/../.." && pwd)"
  tpl="$root_dir/scripts/templates"

  # BASH_SOURCE is a bashism. Sourced from zsh or dash it is empty, dirname
  # returns ".", and root_dir silently resolves to the repository's PARENT --
  # at which point every path below is wrong and the errors point somewhere
  # confusing. Say so here instead.
  [ -d "$tpl" ] || { echo "FATAL: template directory '$tpl' not found." >&2
    echo "       This library is bash-only; source it from bash, not sh/zsh." >&2
    return 1; }

  [ -f "$payload" ] || { echo "FATAL: payload '$payload' not found." >&2
    echo "       Build it first: scripts/build-bench-payload.sh --distro <distro>" >&2
    return 1; }

  # Three things differ between the families: what the redis unit is called,
  # where MariaDB reads drop-in configuration, and where a *package* is allowed
  # to put unit files (/etc/systemd/system belongs to the administrator on both,
  # and installing there would shadow any local override).
  case "$family" in
    debian) local redis_svc=redis-server.service \
                  mariadb_cnf_dir=/etc/mysql/mariadb.conf.d \
                  unit_dir=/lib/systemd/system ;;
    rhel)   local redis_svc=redis.service \
                  mariadb_cnf_dir=/etc/my.cnf.d \
                  unit_dir=/usr/lib/systemd/system ;;
    *) echo "FATAL: unknown family '$family'" >&2; return 1 ;;
  esac
  local mariadb_svc=mariadb.service
  local node_bin="$BENCH_DIR/node/bin/node"

  rm -rf "$dest"
  mkdir -p "$dest$unit_dir" \
           "$dest/etc/frappium/nginx" \
           "$dest$mariadb_cnf_dir" \
           "$dest/usr/bin" \
           "$dest/usr/share/doc/frappium"

  # --- 1. the bench itself -------------------------------------------------
  echo "==> unpacking payload into $dest"
  mkdir -p "$dest/opt"
  tar -xzf "$payload" -C "$dest"
  # A payload tarred on macOS carries AppleDouble "._name" metadata siblings.
  # rpmbuild then aborts with "Installed (but unpackaged) file(s) found" naming
  # a file nobody put there, which is a genuinely baffling error to debug.
  find "$dest" -name '._*' -delete 2>/dev/null || true
  # Every one of these is an ExecStart target. A payload missing any of them
  # produces a package that installs cleanly and then fails at boot -- which is
  # precisely the failure this rewrite exists to eliminate, and the reason it
  # went unnoticed for so long. Check here, where it is a build error.
  local required="env/bin/bench env/bin/gunicorn node/bin/node sites/apps.txt"
  local missing="" f
  for f in $required; do
    [ -e "$dest$BENCH_DIR/$f" ] || missing="$missing $f"
  done
  # Frappe v16 needs python 3.14, which no target distribution ships, so the
  # interpreter travels with the package. Without it the venv's pyvenv.cfg points
  # at nothing and every unit dies on the first import.
  [ -x "$dest$PYTHON_HOME/bin/python3.14" ] || missing="$missing $PYTHON_HOME/bin/python3.14"
  [ -z "$missing" ] || {
    echo "FATAL: payload is incomplete -- missing:$missing" >&2
    echo "       Rebuild it with scripts/build-bench-payload.sh." >&2
    return 1; }

  # --- 2. systemd units ----------------------------------------------------
  # Substitute the two things that genuinely differ between distributions (the
  # redis unit name) and the two that are layout constants, so the shipped units
  # contain no placeholders at all.
  local unit
  for unit in frappium.target frappe-web.service 'frappe-worker@.service' \
              frappe-scheduler.service frappe-socketio.service; do
    sed -e "s|@BENCH@|$BENCH_DIR|g" \
        -e "s|@MARIADB_SVC@|$mariadb_svc|g" \
        -e "s|@REDIS_SVC@|$redis_svc|g" \
        -e "s|@NODE@|$node_bin|g" \
        "$tpl/$unit" > "$dest$unit_dir/$unit"
    chmod 644 "$dest$unit_dir/$unit"
    if grep -q '@[A-Z_]*@' "$dest$unit_dir/$unit"; then
      echo "FATAL: unsubstituted placeholder left in $unit:" >&2
      grep -n '@[A-Z_]*@' "$dest$unit_dir/$unit" >&2
      return 1
    fi
  done

  # --- 3. nginx vhost template --------------------------------------------
  # Derived from the container template rather than copied by hand, so a fix
  # made for the images reaches the packages too. Exactly two lines change:
  # the document root, and the listen port.
  # Two locations, one behaviour. In the images repository the canonical template
  # is right there under upload/; in the standalone packaging repository it is
  # vendored, and scripts/check-nginx-template-drift.sh fails the build if the
  # vendored copy falls behind. Looking in both keeps this file identical in both
  # repositories, so it cannot fork.
  local src_tpl="$root_dir/upload/src/nginx/frappe.conf.template"
  [ -f "$src_tpl" ] || src_tpl="$root_dir/vendor/nginx/frappe.conf.template"
  [ -f "$src_tpl" ] || {
    echo "FATAL: nginx template not found. Looked in:" >&2
    echo "         $root_dir/upload/src/nginx/frappe.conf.template" >&2
    echo "         $root_dir/vendor/nginx/frappe.conf.template" >&2
    return 1; }

  {
    # Name the file it was actually produced from -- in the packaging repository
    # that is the vendored copy, not upload/, and pointing an operator at a path
    # that does not exist is worse than saying nothing.
    cat <<EOF
# GENERATED -- do not edit here.
#
# Produced by scripts/lib/stage-native-root.sh from
#   ${src_tpl#"$root_dir/"}
# (the same template the container images use), with two substitutions: the
# document root and the listen port become variables. Rendered to
# /etc/nginx/conf.d/frappe.conf by \`frappium-setup\`.
#
# Change that template, not this file. If it is a vendored copy, keep it in sync
# with scripts/check-nginx-template-drift.sh.
EOF
    # [0-9][0-9]* rather than [0-9]\+ -- BSD sed does not understand \+, and this
    # gets run on a maintainer's mac often enough to matter.
    sed -e 's|^\( *\)root /home/frappe/frappe-bench/sites;|\1root ${BENCH_SITES};|' \
        -e 's|^\( *\)listen [0-9][0-9]*;|\1listen ${NGINX_PORT};|' \
        "$src_tpl"
  } > "$dest/etc/frappium/nginx/frappe.conf.template"

  # A silent no-op sed here would ship a vhost serving the container's paths.
  grep -q 'root \${BENCH_SITES};'  "$dest/etc/frappium/nginx/frappe.conf.template" || {
    echo "FATAL: nginx template root substitution did not apply" >&2; return 1; }
  grep -q 'listen \${NGINX_PORT};' "$dest/etc/frappium/nginx/frappe.conf.template" || {
    echo "FATAL: nginx template listen substitution did not apply" >&2; return 1; }

  # --- 4. mariadb tuning ---------------------------------------------------
  install -m 644 "$tpl/mariadb-frappe.cnf" "$dest$mariadb_cnf_dir/10-frappe.cnf"

  # --- 5. setup tool + operator overrides ---------------------------------
  install -m 755 "$tpl/frappium-setup" "$dest/usr/bin/frappium-setup"

  cat > "$dest/etc/frappium/frappium.env" <<EOF
# Overrides for the frappe-* systemd units. Uncomment, edit, then:
#   systemctl daemon-reload && systemctl restart frappium.target
#
# Bind gunicorn somewhere else. Keep it on loopback unless nothing is proxying
# in front of it -- frappe.app:application has no authentication of its own
# beyond the application's, and no TLS.
#FRAPPE_WEB_BIND=127.0.0.1:8000
#FRAPPE_WEB_WORKERS=4
#FRAPPE_WEB_THREADS=4
#FRAPPE_WEB_TIMEOUT=120
EOF
  chmod 644 "$dest/etc/frappium/frappium.env"

  install -m 644 "$root_dir/docs/native-install.md" \
                 "$dest/usr/share/doc/frappium/native-install.md" 2>/dev/null || true

  echo "==> staged $(du -sh "$dest" | cut -f1) into $dest"
}
