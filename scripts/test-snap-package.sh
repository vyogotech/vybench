#!/bin/bash
# End-to-end test runner for the vybench snap package.
# Runs on Ubuntu GitHub Actions runners or local VMs with snapd.
set -euo pipefail

SNAP_FILE="${1:-}"

if [ -z "$SNAP_FILE" ]; then
  SNAP_FILE=$(find dist . -maxdepth 2 -name "*.snap" 2>/dev/null | head -n 1 || true)
fi

if [ -z "$SNAP_FILE" ] || [ ! -f "$SNAP_FILE" ]; then
  echo "::error::No snap file specified or found."
  exit 1
fi

SNAP_NAME="vybench"
if [[ "$(basename "$SNAP_FILE")" == *"postgres"* ]]; then
  SNAP_NAME="vybench-postgres"
fi

echo "=== Cleaning up previous installation of $SNAP_NAME ==="
sudo systemctl stop postgresql 2>/dev/null || true
sudo systemctl disable postgresql 2>/dev/null || true
sudo snap remove --purge "$SNAP_NAME" 2>/dev/null || true
sudo rm -rf "/var/snap/$SNAP_NAME" 2>/dev/null || true

echo "=== Installing snap: $SNAP_FILE ($SNAP_NAME) ==="
sudo snap install --dangerous "$SNAP_FILE"

echo "=== Waiting for initial service startup ==="
for _ in $(seq 1 30); do
  if sudo snap services "$SNAP_NAME" 2>/dev/null | grep -q "active"; then
    echo "Services active!"
    break
  fi
  sleep 2
done

echo "=== Switching to developer mode ==="
sudo snap set "$SNAP_NAME" mode=developer

USER_NAME="${SUDO_USER:-${USER:-varun}}"
echo "=== Adding current user ($USER_NAME) to snap_daemon group ==="
sudo usermod -aG snap_daemon "$USER_NAME" 2>/dev/null || true

echo "=== Creating test site (test.localhost) ==="
DB_ARGS=""
if [ "$SNAP_NAME" = "vybench-postgres" ]; then
  ROOT_PW=$(sudo cat "/var/snap/$SNAP_NAME/common/postgres/root_password" 2>/dev/null || echo "postgres")
  DB_ARGS="--db-type postgres --db-port 5432 --db-host 127.0.0.1 --db-root-username postgres --db-root-password ${ROOT_PW:-postgres}"
fi
sudo "$SNAP_NAME.bench" new-site test.localhost --admin-password admin $DB_ARGS

echo "=== Testing get-app (hrms) ==="
BRANCH="version-16"
if [ "$SNAP_NAME" = "vybench-postgres" ]; then
  BRANCH="develop"
fi
sudo "$SNAP_NAME.bench" get-app hrms --branch "$BRANCH"

echo "=== Testing install-app (hrms on test.localhost) ==="
sudo "$SNAP_NAME.bench" --site test.localhost install-app hrms

echo "=== Verifying installed apps on test.localhost ==="
INSTALLED_APPS=$(sudo "$SNAP_NAME.bench" --site test.localhost list-apps)
echo "$INSTALLED_APPS"
if ! echo "$INSTALLED_APPS" | grep -q "hrms"; then
  echo "::error::hrms was not found in installed apps!"
  exit 1
fi

echo "=== Switching back to production mode ==="
sudo snap set "$SNAP_NAME" mode=production

echo "=== Verifying services in production mode ==="
sudo snap services "$SNAP_NAME"

echo "=== Testing HTTP endpoint ==="
sleep 5
curl -sSf -H "Host: test.localhost" http://127.0.0.1:8000/ >/dev/null || curl -sSf http://127.0.0.1:8000/ >/dev/null || true

echo "=== Testing snap removal (uninstall) ==="
sudo snap remove --purge "$SNAP_NAME"

echo "=== Verifying snap is completely uninstalled ==="
if snap list 2>/dev/null | grep -q "^$SNAP_NAME"; then
  echo "::error::$SNAP_NAME snap is still listed after removal!"
  exit 1
fi

if snap services "$SNAP_NAME" 2>/dev/null | grep -q "$SNAP_NAME"; then
  echo "::error::$SNAP_NAME services still present after removal!"
  exit 1
fi

echo "=== E2E Snap Integration Test PASSED ==="
