# Frappista — Native `.deb` / `.rpm` Installation

Frappe v16 and ERPNext as a distribution package: the bench, its virtualenv, its
prebuilt assets and a bundled Python 3.14, plus systemd units for the web server,
all three worker queues, the scheduler and the realtime server.

The package is ~530 MB and installs to ~2.2 GB.

Unlike the [snap](snap-install.md), which bundles everything so that one artefact
runs anywhere, these packages use the distribution's own MariaDB, Redis and
nginx. That is the point of a native package — and the reason each artefact is
tied to one distribution release.

---

## 1. Which package

Each package is built against one distribution release and is not portable
between them. There is no universal build — use the snap if you want one
artefact everywhere.

| Distribution | Package |
| :--- | :--- |
| Ubuntu 24.04 | `frappista_16.0.0-1~ubuntu2404_amd64.deb` |
| Debian 12 | `frappista_16.0.0-1~debian12_amd64.deb` |
| Rocky / Alma / RHEL 9 | `frappista-16.0.0-1.rockylinux9.el9.x86_64.rpm` |
| Fedora 41+ | `frappista-16.0.0-1.fedora41.fc41.x86_64.rpm` |

`arm64` / `aarch64` builds exist for the same set. The distribution appears in
the version, so the file you have always says which build it is — `dpkg -l` and
`rpm -q` show it too.

**Python is bundled.** Frappe v16 requires exactly Python 3.14
(`requires-python = ">=3.14,<3.15"`), and no distribution here ships it — Ubuntu
24.04 has 3.12, Debian 12 has 3.11, Fedora 41 has 3.13, RHEL 9 has 3.9. So the
package carries its own interpreter at `/usr/lib/frappista/python` and does not
touch the system `python3`. Your distribution's Python is left entirely alone.

That interpreter is still compiled against the release's own OpenSSL and sqlite,
which is why the package is release-specific even though Python travels with it.

---

## 2. Install

```bash
# Debian / Ubuntu
sudo apt install ./frappista_16.0.0-1~ubuntu2404_amd64.deb

# Fedora / RHEL / Rocky
sudo dnf install ./frappista-16.0.0-1.rockylinux9.el9.x86_64.rpm
```

Installing does **not** start anything. It unpacks the bench, creates the
`frappe` service account, and stops. Creating a database and a site are
decisions, not side effects of `apt install`.

---

## 3. Set up

```bash
sudo frappista-setup --site dev.localhost --admin-password admin
```

That single command:

1. starts and enables MariaDB and Redis;
2. creates a dedicated MariaDB administrator (`frappe_admin`) with a generated
   password, recorded in `sites/common_site_config.json`;
3. creates the site, **installs every app the package ships** (ERPNext included)
   and enables its scheduler;
4. renders `/etc/nginx/conf.d/frappe.conf`, adds nginx's account to the `frappe`
   group and restarts nginx;
5. enables and starts the six application units.

> Step 3 installs the apps deliberately. `bench new-site` installs Frappe and
> nothing else, so without it an ERPNext package would hand you a bare Frappe
> desk — the app sitting on disk, absent from the site, with no error anywhere.
> `--no-install-apps` gives you the Frappe-only site if that is what you want.
> Installing ERPNext adds a few minutes to first setup.

> Step 4 adds `www-data` (or `nginx`) to the `frappe` group because nginx serves
> `/assets` and `/files` straight off disk, and the bench is deliberately not
> world-readable — `sites/*/private` holds uploaded documents and
> `site_config.json` holds the database password. It **restarts** rather than
> reloads nginx, because supplementary groups are only picked up when a process
> starts.

Options:

```
--site NAME            site to create          (default dev.localhost)
--admin-password PW    Administrator password  (default admin)
--nginx-port PORT      vhost port              (default 80)
--queues "a b c"       worker queues           (default "default short long")
--no-site --no-install-apps --no-nginx --no-services
```

It is idempotent — re-run it after adding a site, changing the port, or
reinstalling.

> The MariaDB `root` account is left alone. On both families it uses
> `unix_socket` authentication, and giving it a password would break `mysql` for
> the system administrator. Frappe gets its own account instead.

---

## 4. Reach the site

```bash
echo "127.0.0.1 dev.localhost" | sudo tee -a /etc/hosts
xdg-open http://dev.localhost/
```

Log in as `Administrator`.

The nginx vhost matches on the `Host` header, so the site name has to resolve
to the server. Any name ending in `.localhost` resolves to loopback on most
systems without an `/etc/hosts` entry; from another machine, point real DNS at
the server or add the entry there.

> [!NOTE]
> On Debian and Ubuntu, `frappista-setup` retires the stock nginx site by moving
> `/etc/nginx/sites-enabled/default` to
> `/etc/nginx/sites-available/default.frappista-disabled`. It holds
> `default_server` on port 80, so Frappe would never receive a request while it
> is enabled. This is what `bench setup production` does too. Removing the
> package puts it back.

---

## 5. Services

| Unit | What it does | Symptom if missing |
| :--- | :--- | :--- |
| `frappe-web.service` | gunicorn on `127.0.0.1:8000` | nothing serves |
| `frappe-worker@default` | background jobs | jobs queue and never run |
| `frappe-worker@short` | short queue | silent — no error anywhere |
| `frappe-worker@long` | long queue | silent — no error anywhere |
| `frappe-scheduler.service` | `bench schedule` | no cron, no email flush |
| `frappe-socketio.service` | realtime server | no live updates or progress bars |

```bash
systemctl status frappe-web frappe-scheduler frappe-socketio
systemctl status 'frappe-worker@*'
systemctl restart frappista.target      # everything at once
journalctl -u frappe-web -f
```

`frappe-worker@` is a systemd template unit, so an extra queue costs one
command:

```bash
sudo systemctl enable --now frappe-worker@myqueue
```

### Tuning

`/etc/frappista/frappista.env` is a conffile read by every unit:

```bash
FRAPPE_WEB_WORKERS=8
FRAPPE_WEB_TIMEOUT=300
```

```bash
sudo systemctl daemon-reload && sudo systemctl restart frappista.target
```

---

## 6. Running `bench`

The bench belongs to the `frappe` service account:

```bash
sudo -u frappe -H bench --site dev.localhost migrate
sudo -u frappe -H bench --site dev.localhost install-app erpnext
sudo -u frappe -H bench new-site second.localhost --admin-password admin
```

`/usr/local/bin/bench` is symlinked to the bench's own CLI at install time — but
only if that path is free, so an existing pip-installed `frappe-bench` is never
silently replaced. Check with `which -a bench`.

`-H` matters: without it `sudo` keeps your own `$HOME`, and bench writes cache
and config into it.

For hands-on development, add yourself to the group instead of prefixing every
command — the same trade as Docker's `usermod -aG docker $USER`:

```bash
sudo usermod -aG frappe $USER
newgrp frappe
```

Membership grants full read/write access to every site's files and to the
database credentials in `common_site_config.json`. Grant it only to people
entitled to that.

---

## 7. Upgrading

Two separate things, and it matters which one you mean.

**Frappe itself** — use bench, exactly as on a `bench init` install. The payload
is an ordinary writable bench with intact git checkouts:

```bash
sudo -u frappe -H bench update
sudo systemctl restart frappista.target
```

**The package** — `apt upgrade` / `dnf update` replaces the payload. Anything
you changed under `apps/` or `env/` is overwritten; `sites/` is not.

Pick one and stay with it. A bench that has been `bench update`-ed and then
package-upgraded is in whichever state the package shipped.

---

## 8. Removing

```bash
sudo apt remove frappista        # or: sudo dnf remove frappista
```

Sites, uploaded files and the MariaDB databases are **not** removed, on remove
or on purge. They are your data, and the package manager cannot tell
"uninstalling" from "about to reinstall". Delete them by hand:

```bash
sudo rm -rf /opt/frappe-bench
sudo mysql -e "DROP DATABASE \`_xxxxxxxx\`"   # the site's db, from site_config.json
```

Take a backup first: `sudo -u frappe -H bench --site dev.localhost backup`.

---

## 9. Building the packages yourself

The bench is built inside a container of the target distribution, at
`/opt/frappe-bench` — the exact path the package installs to. That is what makes
the venv shebangs, `pyvenv.cfg` and the `sites/assets` symlinks correct by
construction rather than by post-processing.

```bash
# 1. payload (~25-40 min, needs podman or docker)
#    Compiles CPython 3.14, runs bench init, and builds the frontend assets.
scripts/build-bench-payload.sh --distro ubuntu:24.04 --apps erpnext

# 2. package
scripts/package-deb.sh                      # picks up dist/payload-*.tar.gz
scripts/package-rpm.sh                      # rhel payloads; needs rpmbuild
```

Or `make package-deb` / `make package-rpm` / `make package-payload`.

> [!WARNING]
> Verify on a **clean** container, never on the build host. Absolute build-time
> paths are invisible on the machine that produced them, because those paths
> still exist there. That is precisely how the snap's dangling `assets/frappe`
> symlink survived until it was installed somewhere else.

See [docs/native-packaging-plan.md](native-packaging-plan.md) for the design and
for the ten defects these packages are built to avoid.
