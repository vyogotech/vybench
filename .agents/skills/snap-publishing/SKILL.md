---
name: snap-publishing
description: >-
  Pre-flight validation and guidelines for building, validating, and publishing Linux snaps
  via Snapcraft. Use when preparing a snap release, inspecting snapcraft.yaml, verifying
  snap payload symlinks, or troubleshooting snap store push failures.
---

# Snap Publishing & Validation Skill

This skill provides step-by-step procedures, store compliance guidelines, and pre-flight validation checks for building and publishing Linux snap packages with Snapcraft.

## Guidelines for Snap Store Compliance

1. **No External or Build-Tree Symlinks**:
   - Symlinks in the snap package MUST NOT point to build host locations such as `/root/parts/*`, `/build/*`, `$SNAPCRAFT_PART_INSTALL/*`, or `/tmp/*`.
   - All symlinks inside `$SNAP` must be relative links (e.g. `../../apps/foo`) or point within `$SNAP` / `$SNAP_COMMON` / `$SNAP_DATA`.
   - Node.js `node_modules` symlinks (e.g. `apps/<app>/<app>/public/node_modules`) are created by yarn during build and MUST be cleaned up dynamically (`find $SNAPCRAFT_PART_INSTALL/opt/frappe-bench/apps -type l -name "node_modules" -delete`).

2. **Clean Shebangs & Interpreter Paths**:
   - Venv executables in `opt/frappe-bench/env/bin/` must use `#!/usr/bin/env python3` or relative interpreter references, not `/root/parts/...`.
   - `pyvenv.cfg` must point to revision-stable runtime paths (`/snap/vybench/current/usr/bin`), never build container paths.

3. **Strict Confinement & Security**:
   - Confinement must be `strict`.
   - Services drop privileges from `root` to `snap_daemon` via `setpriv`.

4. **Permissions**:
   - All files under `$SNAP/opt` must be globally readable (`chmod -R a+rX`).

---

## Pre-Flight Validation Workflow

Before tagging a release or pushing a snap to the Snap Store, run the validation workflow:

### Step 1: Run Pre-Flight Validation Script
Run the validation script on `snapcraft.yaml` or a built payload/package:

```bash
./scripts/validate-snap.sh
```

To validate an extracted `.snap` file or staged build directory:
```bash
./scripts/validate-snap.sh dist/vybench_amd64.snap
# OR
./scripts/validate-snap.sh parts/frappe-bench/install
```

### Step 2: Verify `snapcraft.yaml` Integrity
Check that `snap/snapcraft.yaml` contains:
- Dynamic `node_modules` cleanup across all installed apps.
- A build-time assertion checking for remaining `/root/parts/*` or `$SNAPCRAFT_PART_INSTALL/*` symlinks.

### Step 3: Test Local Build
Pack the snap locally using:
```bash
make snap
# OR
snapcraft pack --output=dist/vybench_$(uname -m).snap
```
Then validate the built package artifact with `scripts/validate-snap.sh`.

---

## Troubleshooting Snap Store Push Failures

| Error Message | Cause | Resolution |
| :--- | :--- | :--- |
| `package contains external symlinks` | A symlink points outside `$SNAP` or to `/root/parts/...` | Run `find ... -name "node_modules" -delete` or relink absolute links to relative (`../../`). |
| `LXD requires additional permissions` | Runner group membership issue | Use `snapcore/action-build` GitHub Action which provisions LXD properly. |
| `Classic confinement review required` | `confinement: classic` used | Switch to `confinement: strict` and drop daemon privileges to `snap_daemon`. |
