# Frappium

Frappe v16 / ERPNext, packaged.

Builds Frappe v16 / ERPNext as installable native packages:

| Artefact | Target | Status |
| :--- | :--- | :--- |
| `.deb` | Ubuntu 24.04, Debian 12 | verified end to end |
| `.snap` | any distribution with `snapd` | verified end to end |
| `.rpm` | Rocky / Alma / RHEL 9 | builds; not yet exercised in a live container |

## Build

```bash
# 1. payload -- compiles CPython 3.14, runs bench init, builds assets (~40 min)
scripts/build-bench-payload.sh --distro ubuntu:24.04 --apps erpnext

# 2. package
scripts/package-deb.sh
scripts/package-rpm.sh          # rhel payloads; needs rpmbuild

# 3. verify -- installs into a clean systemd container and asserts it runs
scripts/test-native-package.sh --distro ubuntu:24.04
```

Never ship a package that step 3 has not passed. The defect this whole project
exists to fix was a `.deb` that installed perfectly and did nothing, because
nobody ran it.

## Install

See [docs/native-install.md](docs/native-install.md) and
[docs/snap-install.md](docs/snap-install.md). The design and the full defect list
are in [docs/native-packaging-plan.md](docs/native-packaging-plan.md).

## The vendored nginx template

`vendor/nginx/frappe.conf.template` is a copy of the container images'
`upload/src/nginx/frappe.conf.template`. The two must agree on how `/assets`,
`/files`, `/socket.io` and the X-Accel-Redirect handoff are routed.

**Right now this copy is authoritative.** The images repository does not track
`upload/` — it is untracked there, so no URL resolves to the original and this is
the only version-controlled copy that exists.

```bash
scripts/check-nginx-template-drift.sh            # reports the above, passes
scripts/check-nginx-template-drift.sh --local /path/to/frappe.conf.template
scripts/check-nginx-template-drift.sh --update   # adopt an upstream change
```

Once the images repository commits `upload/`, set `UPSTREAM_URL` (and
`GITHUB_TOKEN` if it is private) and the check starts comparing for real.

## CI

One workflow, `.github/workflows/build.yml`. Every build job runs **in parallel**:

| Job | Produces |
| :--- | :--- |
| `deb · ubuntu:24.04` | `.deb` — payload, package, then verified in a clean container |
| `deb · debian:12` | same |
| `snap · amd64` | `.snap` via Canonical's `snapcore/action-build` |
| `snap · arm64` | opt-in (see below) |
| `release` | GitHub release — waits for all of the above |
| `publish-snap` | Snap Store, `edge` channel |

Snaps are built with the **official** `snapcore/action-build@v1`, which
provisions LXD correctly. Hand-rolling it does not work: `usermod -aG lxd $USER`
followed by `newgrp lxd` inside a single `run:` step cannot affect later steps,
so snapcraft fails with *"LXD requires additional permissions"*. `build-info` is
disabled, so no `snap/manifest.yaml` enumerating the build inputs ships inside a
private artefact.

**arm64 is opt-in.** GitHub's arm64 runners are unavailable to private
repositories on the Free plan, and an unavailable runner label queues forever
instead of failing. Tag builds include it; manual runs need
`include_arm64: true`.

### Cost

A full run is ~2 hours of runner time — each `.deb` job compiles CPython 3.14
from source, runs `bench init` and builds the frontend assets, and the snap job
does the same inside LXD. On a **private** repository those minutes are metered
(2,000/month on Free, 3,000 on Team).

So the workflow triggers on **tags and manual dispatch only**, never on push, and
`concurrency` cancels a superseded run rather than letting two two-hour builds
race. Iterate locally with `scripts/build-bench-payload.sh` — the same code path
CI uses.

## Licensing

Frappe Framework is MIT. **ERPNext is GPL-3.0**, so a package containing it may
be sold, but recipients are entitled to the corresponding source and may
redistribute. The payload ships `apps/erpnext` as a full git checkout, which
satisfies that by construction. This repository being private is unaffected —
GPL has never covered build tooling.
