# Implementation Plan: Native `.deb` and `.rpm` Packages

Ship Frappe v16 as native distribution packages for Debian/Ubuntu (`.deb`) and
Fedora/RHEL (`.rpm`), reusing the service topology and the defect fixes proven
while building the `vybench` snap.

---

> [!NOTE]
> **Status: implemented.** Everything in §7 exists. This document is kept as the
> rationale — why each piece is shaped the way it is, and which failure it
> prevents. For using the packages, see [native-install.md](native-install.md).

## 1. Starting Point

### 1.1 What existed

| Artefact | State |
| :--- | :--- |
| `scripts/package-deb.sh` | Stub — built a package that **could not work** (see below) |
| `scripts/templates/frappe-web.service` | Present, incomplete |
| `scripts/templates/frappe-worker.service` | Present, only the `default` queue |
| `scripts/templates/mariadb-frappe.cnf` | Fine as-is |
| RPM packaging | Did not exist |

### 1.2 The blocking defect

`package-deb.sh` created `/opt/frappe-bench` and never put anything in it:

```bash
mkdir -p "${STAGING_DIR}/opt/frappe-bench"   # ...and nothing was ever copied here
```

The resulting `.deb` contained no `apps/`, no `env/`, no `sites/`.
`frappe-web.service` starts `/opt/frappe-bench/env/bin/gunicorn`, which did not
exist, so the unit failed on boot. The package installed cleanly and produced a
non-functional system — the worst failure mode, because `dpkg -i` reports
success.

This is now guarded in three places, because it is the failure that is easiest to
reintroduce and hardest to notice:

- `stage_native_root` refuses to stage a payload missing `env/bin/bench`,
  `env/bin/gunicorn`, `node/bin/node` or `sites/apps.txt`;
- the `%install` section of the spec asserts the same;
- `scripts/test-native-package.sh` installs the built package in a clean
  container and checks that every unit reaches `active`.

---

## 2. Defects Carried Over From the Snap

Each of these was found by *running* the snap, not by reading it. The `.deb`/`.rpm`
path reproduces all of them unless fixed deliberately.

| # | Defect | How it presents | Fix for native packages |
| :-- | :--- | :--- | :--- |
| 1 | `sites/config/pids` missing | `bench worker` dies with `Error: No such command 'worker'`. bench's `is_bench_directory()` requires **all** of `("apps","sites","config","logs","config/pids")`; miss one and frappe's subcommands are never loaded | Create `config/pids` in the package payload |
| 2 | `sites/apps.txt` missing | Same symptom as #1 — bench cannot discover installed apps | Ship `sites/apps.txt` and `sites/apps.json` |
| 3 | Only the `default` queue has a worker | Jobs enqueued to `short`/`long` are accepted and never run. Silent | Three units: `frappe-worker@default/short/long` |
| 4 | No scheduler | No scheduled jobs, no email queue flush, no auto-repeat. Silent | `frappe-scheduler.service` running `bench schedule`, plus `bench --site X scheduler enable` |
| 5 | No socketio | No realtime UI: live updates, notifications, progress bars dead | `frappe-socketio.service` running `node apps/frappe/socketio.js` |
| 6 | `frappe.app:application` serves no static files | Every `/assets/**` request 404s; desk renders unstyled | Native packages have nginx — use it (see §4). Do **not** paper over it with `application_with_statics()`, which is a snap-only workaround for having no proxy |
| 7 | Empty MariaDB root password | `bench new-site` blocks on `getpass()` and dies with `EOFError` under systemd — frappe treats a falsy `root_password` as "prompt me" | Generate a random password at first setup, write it to `common_site_config.json` (see §6.1 — a *dedicated* account, not `root`) |
| 8 | `pyvenv.cfg` `home` points at the build interpreter | CPython resolves the stdlib against the build host; on a different machine you get missing modules or ABI errors | Build the venv at the **final install path** so the recorded paths are already correct (see §3.2) |
| 9 | `sites/assets/<app>` are absolute symlinks into the build tree | Dangling on the target; all assets 404 | Build at the final path, and assert no symlink escapes the payload |
| 10 | Running services as root | bench refuses to run as root; `change_uid()` raises `KeyError: getpwnam` when the configured user does not exist | Create a real `frappe` system user in `postinst`/`%pre`; run every unit as it |

> [!IMPORTANT]
> Defects 8 and 9 are the same root cause — build-time absolute paths leaking into a
> shipped artefact. In the snap they had to be rewritten after the fact. Native packages
> avoid the whole class **for free** by building at `/opt/frappe-bench`, the same path
> the package installs to. Do not build in a temp directory and relocate.

---

## 3. Architecture

### 3.1 Divergence from the snap — deliberate

The snap bundles everything because it must run on any distribution. Native packages
should do the opposite and use the distribution's own components; that is the point of
a native package.

| Component | Snap | `.deb` / `.rpm` |
| :--- | :--- | :--- |
| MariaDB, Redis, Nginx | bundled in the squashfs | `Depends:` / `Requires:` on system packages |
| Python | bundled 3.14 | **also bundled 3.14** — forced, see §3.3 |
| Node.js | bundled 24 | bundled in the payload (distro versions are too old/variable) |
| Process supervision | snapd daemons | systemd units |
| Service account | `snap_daemon` (created by snapd) | `frappe` (created by the package) |
| Upgrade | `snap refresh` | `apt upgrade` / `dnf update` |

### 3.2 Payload built at the install path

The package payload is produced by running `bench init` **inside a build container of
the target distribution**, at `/opt/frappe-bench` — the exact path the package installs
to. This makes venv shebangs, `pyvenv.cfg`, and asset symlinks correct by construction
rather than by post-processing.

Consequence, and it is unavoidable: **the artefact is distribution-specific.** Even
with Python bundled (§3.3), that interpreter is compiled against the release's own
OpenSSL, libffi and sqlite, so one `.deb` cannot serve both Ubuntu 24.04 and
Debian 12. The build matrix is per distro release, unlike the snap's single
universal artefact.

### 3.3 Python version — the plan was wrong here

The original version of this section said "Frappe v16 requires Python 3.11+, so use
what the distribution ships." That is false, and the first real build proved it:

```
ERROR: Package 'frappe' requires a different Python: 3.12.3 not in '<3.15,>=3.14'
```

Frappe v16's `pyproject.toml` pins `requires-python = ">=3.14,<3.15"` — exactly
3.14, not 3.11+. (v15 allowed `>=3.10,<3.15`, which is where the 3.11+ belief came
from.) **No distribution in the matrix ships 3.14:**

| Distribution | System python3 | Can run frappe v16? |
| :--- | :--- | :--- |
| Ubuntu 24.04 | 3.12 | no |
| Debian 12 | 3.11 | no |
| Fedora 41 | 3.13 | no |
| RHEL / Rocky 9 | 3.9 (3.11 available) | no |

So the interpreter is **bundled**: `build-bench-payload.sh` compiles CPython 3.14
from source into `/usr/lib/vybench/python` inside the build container, and the
package ships it. The alternative — depending on the deadsnakes PPA — is not
acceptable for a distribution package.

Three consequences worth stating plainly:

1. **It is not installed under `/opt/frappe-bench`.** `bench init` refuses a
   non-empty target directory (verified: `ERROR: Bench instance already exists`),
   and the interpreter must already exist at its final path when the venv is
   created, because that path is what `pyvenv.cfg` records. `/usr/lib/vybench`
   is the FHS-correct home for a package-owned private runtime.
2. **Built without `--enable-shared`,** so libpython is linked into the binary.
   It therefore needs no `LD_LIBRARY_PATH` and cannot bind to a different
   libpython on the host — which is the entire class of problem behind the snap's
   `OPENSSL_3.3.0` collision.
3. **Runtime library dependencies are computed, not hardcoded.** The interpreter
   links against this release's OpenSSL, libffi and sqlite; the build resolves
   the owning packages with `dpkg -S` / `rpm -qf` over `ldd` output. Hardcoding
   them would break on Ubuntu 24.04's `t64` renames (`libssl3t64`,
   `libreadline8t64`) and produce an uninstallable package.

> [!WARNING]
> `configure` silently omits any stdlib module whose `-dev` headers are missing —
> no error, just a Python without `ssl` or `sqlite3`, discovered much later at
> runtime. The build asserts that `ssl`, `sqlite3`, `lzma`, `bz2`, `zlib`,
> `ctypes`, `readline`, `hashlib`, `uuid` and `decimal` all import before it
> proceeds.

---

## 4. Nginx

Native packages have a real nginx, so it serves `/assets` and `/files` from disk and
proxies `/socket.io` to node and everything else to gunicorn — the standard Frappe
production topology.

Reuse the existing template rather than writing a third copy:
`upload/src/nginx/frappe.conf.template`, already used by the container images and
adapted for the snap. Fill it with `envsubst` exactly as
`upload/src/nginx/nginx-entrypoint.sh` does, with `BENCH_SITES=/opt/frappe-bench/sites`,
`BACKEND=127.0.0.1:8000`, `SOCKETIO=127.0.0.1:9000`.

Install to `/etc/nginx/conf.d/frappe.conf` and listen on **80**, which is legitimate
here — unlike the snap, systemd can grant a privileged port.

---

## 5. Service Topology

Mirrors what `bench setup production` generates under supervisor, and what the snap
runs under snapd:

| Unit | Command |
| :--- | :--- |
| `frappe-web.service` | `gunicorn -b 127.0.0.1:8000 frappe.app:application` |
| `frappe-worker@.service` | `bench worker --queue %i` — enable for `default`, `short`, `long` |
| `frappe-scheduler.service` | `bench schedule` |
| `frappe-socketio.service` | `node apps/frappe/socketio.js` |

All run `User=frappe`, `Group=frappe`, `After=mariadb.service redis.service`.

`frappe-worker@.service` is a systemd **template unit**, so the three queues are one
file rather than three near-identical copies.

---

## 6. Permissions

The snap's `snap_daemon` + shared-group model exists only because snapd owns the
service account. Native packages own the `frappe` user outright, so the container
image's model applies directly:

- Payload owned `frappe:frappe`, mode `ug+rwX,o-rwx` — **never** world-writable,
  and here not world-*readable* either.
- Administrators run `sudo -u frappe -H bench ...`.
- Optionally add an operator to the `frappe` group for sudo-less access — the same
  trade documented for the snap and for Docker.

Concretely this is the Containerfile's `chown -R 1001:0 . && chmod -R ug+rwX .`, with
`frappe:frappe` in place of `1001:0`.

### 6.1 Two things that only surfaced during implementation

Neither was in the original plan; both are load-bearing.

**nginx needs group membership, not world-readable files.** Unlike the snap —
where gunicorn served static files itself via `application_with_statics()` —
here nginx reads `/assets` and `/files` straight off disk. With `o-rwx` on the
bench it cannot, and every asset 403s. The fix is *not* to open the tree up:
`sites/*/private` holds uploaded documents and `site_config.json` holds the
database password, and the vhost's `internal;` directive is an nginx-level
restriction, not a filesystem one. `vybench-setup` adds nginx's own account
(`www-data` or `nginx`, read from `nginx.conf`) to the `frappe` group instead.

It must then **restart** nginx, not reload it: supplementary groups are read at
process start, so a reload leaves the existing workers without the new group and
the 403s persist — with a config that looks completely correct.

**The database account is `frappe_admin`, not `root`.** Defect 7 needs a
password-authenticated account that can `CREATE DATABASE` and `GRANT`. Giving
MariaDB's `root` a password would satisfy that and break `mysql` for the system
administrator, because on both families `root` authenticates via `unix_socket`.
So `vybench-setup` creates a dedicated account and records it as
`root_login` / `root_password` (and `db_root_username`, which newer bench reads).
This is strictly better than what the snap does, where the generated password
*is* the MariaDB root password.

---

## 7. Deliverables

All present.

| File | Purpose |
| :--- | :--- |
| `scripts/build-bench-payload.sh` | Runs `bench init` at `/opt/frappe-bench` inside a container of the target distro; emits `payload-<distro>-<arch>.tar.gz` plus a `.env` of build metadata |
| `scripts/lib/stage-native-root.sh` | Shared staging: both packages ship the *same* filesystem, so the layout lives in one place rather than drifting between two packagers |
| `scripts/package-deb.sh` | Rewritten — ships the payload, generates `control`/`postinst`/`prerm`/`postrm` |
| `scripts/package-rpm.sh` | Stages the root, tars it as `Source0`, drives `rpmbuild` |
| `packaging/vybench.spec` | RPM spec with `%pre`/`%post`/`%preun`/`%postun` |
| `scripts/templates/frappe-web.service` | Rewritten — `EnvironmentFile`, hardening, `PartOf` |
| `scripts/templates/frappe-worker@.service` | Template unit replacing the single-queue file |
| `scripts/templates/frappe-scheduler.service` | New |
| `scripts/templates/frappe-socketio.service` | New |
| `scripts/templates/vybench.target` | New — start/stop the application tier as one unit |
| `scripts/templates/vybench-setup` | New — first-run: DB account, site, nginx, services |
| `scripts/templates/common_site_config.json` | New — static config baked into the payload *and* shipped, so the two cannot disagree |
| `scripts/test-native-package.sh` | New — installs the built package in a clean systemd container and asserts §8 |
| `docs/native-install.md` | New — the user-facing guide |

The units carry `@BENCH@` / `@MARIADB_SVC@` / `@REDIS_SVC@` / `@NODE@`
placeholders, substituted per family by the stager, which then fails the build if
any placeholder survives. Redis is `redis-server.service` on Debian and
`redis.service` on RHEL; that one difference is the reason the templates exist at
all.

---

## 8. Verification

Reading a package proves nothing — every defect in §2 was found by running one. Each
artefact must be installed in a clean container of its target distribution and checked.

This is automated as `scripts/test-native-package.sh`, which builds a
systemd-capable image of the target distribution, boots it, installs the package,
runs `vybench-setup`, and asserts everything below plus the payload-completeness
and placeholder checks. CI runs it for every row of the matrix. What it does, by
hand:

```bash
# 1. Services actually run, and as frappe (not root)
systemctl is-active frappe-web frappe-scheduler frappe-socketio \
                    frappe-worker@default frappe-worker@short frappe-worker@long
ps -o user= -C gunicorn | sort -u        # must be: frappe

# 2. Site creation is non-interactive (regression test for defect 7)
sudo -u frappe bench new-site dev.localhost --admin-password admin

# 3. HTTP through nginx, and static assets actually resolve (defects 6 and 9)
curl -s -o /dev/null -w '%{http_code}\n' -H 'Host: dev.localhost' http://127.0.0.1/
curl -s -o /dev/null -w '%{http_code}\n' -H 'Host: dev.localhost' \
     http://127.0.0.1/assets/frappe/dist/css/website.bundle.*.css

# 4. Background work reaches every queue (defect 3)
sudo -u frappe bench --site dev.localhost execute \
  frappe.utils.background_jobs.enqueue --kwargs "{'method':'frappe.ping','queue':'long'}"

# 5. No symlink escapes the payload (defect 9)
find /opt/frappe-bench -xtype l        # must print nothing
```

> [!WARNING]
> Verify on a **clean** container, never on the build host. Defects 8 and 9 are
> invisible on the machine that built the package, because the build paths still exist
> there. That is exactly how the snap's dangling `assets/frappe` symlink survived until
> it was installed on a different machine.

### 8.1 What the first real builds actually found

Four payload builds were needed before one succeeded. Every failure was in code
that read correctly:

| # | Failure | Cause |
| :-- | :--- | :--- |
| 1 | `pip install -e apps/frappe` | Frappe v16 needs python **3.14**, not 3.11+ (§3.3) |
| 2 | `bench build`, at the very end of a 25-minute init | `bench` was not on `PATH`; bench shells out to the literal string `bench`, so init failed after all the work and rolled it back |
| 3 | exit 123 at the last step | `xargs` propagating a non-zero from `dpkg -S`, which legitimately fails on files no package owns |
| 4 | dependency list had one entry instead of twelve | `{ sed …; grep …; }` fed from one pipe share a single stdin — `sed` drained it and `grep` silently saw nothing |
| 5 | 14 of 15 library lookups missed | **usrmerge**: `ldd` reports `/lib/<triplet>/libssl.so.3`, dpkg records `/usr/lib/<triplet>/libssl.so.3`. Both spellings have to be queried |

Three more were bugs in the verification itself, and those matter more than they
look — a test that passes for the wrong reason is how the original empty `.deb`
went unnoticed in the first place:

- `find -perm -0002` flagged the venv's `env/lib64 -> lib` symlink. Symlinks are
  always `lrwxrwxrwx`, `chmod` cannot change that, and `find -perm` uses `lstat`.
  Needs `! -type l`.
- The queue check waited for `rq:queue:*` keys to exist with length 0 — but RQ
  **deletes** a queue's key the moment it drains, so a working system could never
  satisfy it. It now asserts jobs were *executed*, via RQ's finished-job registry.
- `bench execute frappe.utils.background_jobs.enqueue` exits non-zero **on
  success**: `enqueue()` returns an `rq.job.Job` and bench then fails to
  JSON-serialise it, after the job is safely queued. Briefly this made the suite
  report "could not enqueue" while the queue check still passed — on
  scheduler-generated jobs rather than the ones it thought it had enqueued.

None of these were visible by reading the code, which is the whole argument for
§8 existing.

### 8.2 Verified result

`vybench_16.31.0-1~ubuntu2404_arm64.deb` (533 MB, frappe 16.31.0 **+ erpnext**,
bundled CPython 3.14.7) passes all 26 checks on a clean `ubuntu:24.04` systemd
container: site created non-interactively with every shipped app installed into
it, all six units active, nothing running as root, nginx serving both the desk
and `/assets` at 200, and all three worker queues executing jobs.

Adding ERPNext surfaced one more silent failure, in the same family as the
original ten: `bench new-site` installs **frappe only**. A package advertising
ERPNext would otherwise deliver a bare Frappe desk, with the app present on disk,
absent from the site, and no error logged anywhere. `vybench-setup` now
installs every app listed in `sites/apps.txt`, and the suite asserts
`bench list-apps` covers them.
