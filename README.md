# Frappista Packaging

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

```bash
scripts/check-nginx-template-drift.sh            # CI runs this
scripts/check-nginx-template-drift.sh --update   # adopt an upstream change
```

Set `UPSTREAM_URL` (and `GITHUB_TOKEN`, if the images repository is private).

## CI cost

A full matrix run is ~100+ minutes of runner time: each payload compiles CPython
from source, runs `bench init`, and builds the frontend assets. On a **private**
repository those minutes are metered — 2,000/month on Free, 3,000 on Team.

The workflow therefore builds on **tags and manual dispatch only**, never on
push. Iterate locally with `scripts/build-bench-payload.sh`; it is the same code
path CI uses.

## Licensing

Frappe Framework is MIT. **ERPNext is GPL-3.0**, so a package containing it may
be sold, but recipients are entitled to the corresponding source and may
redistribute. The payload ships `apps/erpnext` as a full git checkout, which
satisfies that by construction. This repository being private is unaffected —
GPL has never covered build tooling.
