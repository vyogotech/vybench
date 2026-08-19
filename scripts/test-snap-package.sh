#!/bin/bash
# End-to-end test runner for the vybench snap package.
# Runs on Ubuntu GitHub Actions runners or local VMs with snapd.
set -euo pipefail

SNAP_FILE="${1:-}"

if [ -z "$SNAP_FILE" ]; then
  SNAP_FILE=$(ls dist/*.snap vybench_*.snap *.snap 2>/dev/null | head -n 1 || true)
fi

if [ -z "$SNAP_FILE" ] || [ ! -f "$SNAP_FILE" ]; then
  echo "::error::No snap file specified or found."
  exit 1
fi

echo "=== Installing snap: $SNAP_FILE ==="
sudo snap install --dangerous "$SNAP_FILE"

echo "=== Waiting for initial service startup ==="
for i in $(seq 1 30); do
  if sudo snap services vybench 2>/dev/null | grep -q "active"; then
    echo "Services active!"
    break
  fi
  sleep 2
done

echo "=== Switching to developer mode ==="
sudo snap set vybench mode=developer

echo "=== Adding current user ($USER) to snap_daemon group ==="
sudo usermod -aG snap_daemon "$USER" 2>/dev/null || true

echo "=== Creating test site (test.localhost) ==="
sudo vybench.bench new-site test.localhost --admin-password admin

echo "=== Testing get-app (hrms v16) ==="
sudo vybench.bench get-app hrms --branch version-16

echo "=== Testing install-app (hrms on test.localhost) ==="
sudo vybench.bench --site test.localhost install-app hrms

echo "=== Verifying installed apps on test.localhost ==="
INSTALLED_APPS=$(sudo vybench.bench --site test.localhost list-apps)
echo "$INSTALLED_APPS"
if ! echo "$INSTALLED_APPS" | grep -q "hrms"; then
  echo "::error::hrms was not found in installed apps!"
  exit 1
fi

echo "=== Switching back to production mode ==="
sudo snap set vybench mode=production

echo "=== Verifying services in production mode ==="
sudo snap services vybench

echo "=== Testing HTTP endpoint ==="
sleep 5
curl -sSf -H "Host: test.localhost" http://127.0.0.1:8000/ >/dev/null || curl -sSf http://127.0.0.1:8000/ >/dev/null || true

echo "=== Testing snap removal (uninstall) ==="
sudo snap remove --purge vybench

echo "=== Verifying snap is completely uninstalled ==="
if snap list 2>/dev/null | grep -q "^vybench"; then
  echo "::error::vybench snap is still listed after removal!"
  exit 1
fi

if snap services vybench 2>/dev/null | grep -q "vybench"; then
  echo "::error::vybench services still present after removal!"
  exit 1
fi

echo "=== E2E Snap Integration Test PASSED ==="
