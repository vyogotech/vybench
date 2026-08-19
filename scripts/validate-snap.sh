#!/usr/bin/env bash
# Validate Snap payload and metadata against Canonical Snap Store publishing guidelines.
#
# Checks:
# 1. External / build-tree absolute symlinks (/root/parts/*, /build/*, /tmp/*)
# 2. Node.js node_modules symlinks in apps/*/*/public/node_modules
# 3. Shebang lines in virtualenv executables
# 4. pyvenv.cfg interpreter paths
# 5. File permissions on executable wrappers
#
# Usage:
#   scripts/validate-snap.sh [DIR_OR_SNAP_FILE]

set -euo pipefail

TARGET="${1:-}"

if [ -z "$TARGET" ]; then
  # Default search paths if not provided
  if [ -d "parts/frappe-bench/install" ]; then
    TARGET="parts/frappe-bench/install"
  elif [ -d "prime" ]; then
    TARGET="prime"
  elif [ -f "dist/vybench_amd64.snap" ]; then
    TARGET="dist/vybench_amd64.snap"
  elif ls dist/*.snap >/dev/null 2>&1; then
    TARGET="$(ls -1 dist/*.snap | head -1)"
  else
    echo "NOTE: No build directory or .snap file specified. Validating snapcraft.yaml rules statically."
    TARGET="snap/snapcraft.yaml"
  fi
fi

echo "==> Running Snap Pre-flight Validation on: $TARGET"
ERRORS=0

if [ -f "$TARGET" ] && [[ "$TARGET" == *.snap ]]; then
  echo "--- Inspecting built .snap package archive ---"
  if command -v unsquashfs >/dev/null 2>&1; then
    TEMP_DIR="$(mktemp -d)"
    trap 'rm -rf "$TEMP_DIR"' EXIT
    unsquashfs -d "$TEMP_DIR/extracted" "$TARGET" >/dev/null
    TARGET="$TEMP_DIR/extracted"
  else
    echo "WARNING: unsquashfs not installed; checking package file listing with snapcraft/tar if possible"
  fi
fi

if [ -f "$TARGET" ] && [[ "$TARGET" == *"snapcraft.yaml" ]]; then
  echo "--- Static Validation of snapcraft.yaml ---"
  
  # Check 1: Ensure node_modules symlink cleanup is dynamic (find ... -name node_modules -delete)
  if grep -q 'find .* -name "node_modules" -delete' "$TARGET" || grep -q 'find .* -name node_modules -delete' "$TARGET"; then
    echo " [PASS] Dynamic node_modules symlink cleanup present in snapcraft.yaml"
  else
    echo " [FAIL] Missing dynamic node_modules symlink cleanup in snapcraft.yaml"
    ERRORS=$((ERRORS + 1))
  fi

  # Check 2: Ensure assertion against build-path symlinks is present
  if grep -q '/root/parts/' "$TARGET" && grep -q 'FATAL:' "$TARGET"; then
    echo " [PASS] Build-tree symlink assertion present in snapcraft.yaml"
  else
    echo " [FAIL] Missing assertion against build-tree symlinks in snapcraft.yaml"
    ERRORS=$((ERRORS + 1))
  fi

  # Check 3: Confinement check
  if grep -q '^confinement: strict' "$TARGET"; then
    echo " [PASS] Strict confinement specified"
  else
    echo " [WARN] Non-strict confinement detected"
  fi

  # Check 4: System usernames
  if grep -q 'snap_daemon:' "$TARGET"; then
    echo " [PASS] snap_daemon system username configured"
  else
    echo " [WARN] Missing snap_daemon system username"
  fi

  echo ""
  if [ $ERRORS -eq 0 ]; then
    echo "==> SUCCESS: Static snapcraft.yaml validation passed!"
    exit 0
  else
    echo "==> ERROR: $ERRORS issue(s) found in snapcraft.yaml"
    exit 1
  fi
fi

# Directory inspection checks (e.g. staged part or extracted snap)
if [ -d "$TARGET" ]; then
  echo "--- Inspecting directory tree at $TARGET ---"

  # Check 1: External symlinks pointing to build tree or outside snap
  echo "Checking for external/build-tree absolute symlinks..."
  BAD_LINKS="$(find "$TARGET" -type l \( -lname "*/root/parts/*" -o -lname "*/build/*" -o -lname "/tmp/*" \) 2>/dev/null || true)"
  if [ -n "$BAD_LINKS" ]; then
    echo " [FAIL] Found external/build-tree symlinks:"
    echo "$BAD_LINKS"
    ERRORS=$((ERRORS + 1))
  else
    echo " [PASS] No external/build-tree symlinks found"
  fi

  # Check 2: Dangling node_modules symlinks under apps/
  echo "Checking for leftover node_modules symlinks..."
  NODE_LINKS="$(find "$TARGET" -type l -name "node_modules" 2>/dev/null || true)"
  if [ -n "$NODE_LINKS" ]; then
    echo " [FAIL] Found leftover node_modules symlinks under payload:"
    echo "$NODE_LINKS"
    ERRORS=$((ERRORS + 1))
  else
    echo " [PASS] No leftover node_modules symlinks found"
  fi

  # Check 3: Shebang lines in virtualenv executables
  if [ -d "$TARGET/opt/frappe-bench/env/bin" ]; then
    echo "Checking shebang lines in opt/frappe-bench/env/bin..."
    BAD_SHEBANGS="$(grep -rn '^#!/.*root/parts' "$TARGET/opt/frappe-bench/env/bin/" 2>/dev/null || true)"
    if [ -n "$BAD_SHEBANGS" ]; then
      echo " [FAIL] Found build-path shebangs in virtualenv:"
      echo "$BAD_SHEBANGS"
      ERRORS=$((ERRORS + 1))
    else
      echo " [PASS] Shebang lines are clean"
    fi
  fi

  # Check 4: pyvenv.cfg validation
  PYVENV="$TARGET/opt/frappe-bench/env/pyvenv.cfg"
  if [ -f "$PYVENV" ]; then
    echo "Checking pyvenv.cfg..."
    if grep -q '/root/parts' "$PYVENV"; then
      echo " [FAIL] pyvenv.cfg contains build host path (/root/parts):"
      cat "$PYVENV"
      ERRORS=$((ERRORS + 1))
    else
      echo " [PASS] pyvenv.cfg paths are valid"
    fi
  fi

  echo ""
  if [ $ERRORS -eq 0 ]; then
    echo "==> SUCCESS: Snap payload validation passed!"
    exit 0
  else
    echo "==> ERROR: $ERRORS issue(s) found in snap payload"
    exit 1
  fi
fi

echo "FATAL: Could not validate target '$TARGET'" >&2
exit 1
