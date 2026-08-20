# Vybench Snap — Installation

A self-contained Frappe v16 install for any Linux distribution with `snapd`
(Fedora, Ubuntu, Debian, Arch, RHEL). Python 3.14, MariaDB 10.11, Redis 7,
Node.js 24 and Nginx are bundled inside the snap — nothing is taken from the
host, so there are no library or OpenSSL conflicts between distributions.

---

## 1. Install

```bash
sudo snap install vybench
```

Local build:

```bash
sudo snap install --dangerous vybench_16.0.0_amd64.snap
```

Vybench is strictly confined, so it needs no `--classic` flag. The `home`
interface auto-connects on install, which is what lets `bench backup` and
`bench restore` reach files in your home directory. If you keep backups on an
external drive, connect the one interface that is not automatic:

```bash
sudo snap connect vybench:removable-media
```

The install starts MariaDB and Redis, generates a random database root
password, and brings up the full application stack. No post-install step is
required to get a working server.

---

## 2. Use `bench` instead of `vybench.bench`

The snap exposes its CLI as `vybench.bench`. Alias it to the name everyone
expects:

```bash
sudo snap alias vybench.bench bench
```

Check with `snap aliases vybench`; undo with `sudo snap unalias bench`.

> If a pip-installed `frappe-bench` is already present, `/usr/local/bin/bench`
> usually wins over `/snap/bin/bench` in `PATH`. Run `which -a bench` to see
> which one you are getting.

---

## 3. Choose an install mode

```bash
sudo snap set vybench mode=production   # default
sudo snap set vybench mode=developer
```

|                         | production                            | developer                        |
| ----------------------- | ------------------------------------- | -------------------------------- |
| MariaDB, Redis          | running                               | running                          |
| web, workers, scheduler, socketio | running as managed services | **off** — you run `bench serve`  |
| `apps/`, `env/`         | read-only, shared from the snap       | real writable copy (~1.15 GB)    |
| Upgrade with            | `snap refresh` (`snap revert` to roll back) | `bench update`, `bench switch-to-branch` |
| Best for                | servers, demos, CI                    | app development                  |

Switching to `developer` copies `apps/` and `env/` out of the read-only snap so
the bench behaves like an ordinary `bench init` install — `get-app`, `new-app`,
source edits, `pip install`, `build` and `watch` all work. It takes about a
minute and roughly 1.15 GB. Switching back to `production` leaves that copy in
place, so your work is never discarded.

---

## 4. Create a site

```bash
cd /var/snap/vybench/common/bench
bench new-site mysite.localhost --admin-password admin
```

No database password is needed: one is generated at first start and recorded in
`sites/common_site_config.json`. Log in as `Administrator`.

Reach the site at <http://127.0.0.1:8000> — send the site name as the `Host`
header, or add it to `/etc/hosts`:

```bash
echo "127.0.0.1 mysite.localhost" | sudo tee -a /etc/hosts
```

---

## 5. Running `bench` as your own user

**In developer mode this needs no setup.** The bench belongs to the user who
creates it, so `bench` works directly.

**In production mode the services own the bench**, because they run as the
unprivileged `snap_daemon` account rather than as root. A plain user therefore
cannot write to it. Two ways to work with that:

```bash
# Occasional use — run as the service account
sudo bench --site mysite.localhost migrate
```

```bash
# Regular use — grant yourself permanent access (the Docker post-install step)
sudo usermod -aG snap_daemon $USER
newgrp snap_daemon          # or log out and back in
```

This mirrors Docker's `usermod -aG docker $USER`, and carries the same caveat:
membership grants full read/write access to every site's files and database
directory. Grant it only to users who are entitled to that.

`bench` detects this situation and prints these options rather than failing with
a bare permission error.

---

## 6. Optional services

```bash
sudo snap set vybench nginx=true          # reverse proxy, production only
sudo snap set vybench nginx-port=8080     # default 8080
sudo snap set vybench watch=true          # asset rebuilder, developer only
sudo snap set vybench bind=0.0.0.0        # expose the web port on the LAN
```

Nginx is off by default: gunicorn already serves static assets correctly, and
binding a port collides with any web server already on the host. It listens on
8080 rather than 80 because the services run unprivileged and cannot bind ports
below 1024.

---

## 7. Everyday commands

```bash
snap services vybench                     # what is running
sudo snap restart vybench.web
sudo snap logs vybench.web -f
sudo snap get vybench mode

vybench.mysql -u root -p -S /var/snap/vybench/common/run/mysql.sock
vybench.redis-cli ping
```

Data lives in `/var/snap/vybench/common`:

| Path            | Contents                                  |
| --------------- | ----------------------------------------- |
| `bench/sites/`  | sites, site configs, uploaded files       |
| `mariadb/`      | database files (private to the service)   |
| `run/`          | MariaDB socket                            |
| `bench/logs/`   | bench and worker logs                     |

`snap remove vybench` keeps this directory as a snapshot; use
`snap remove --purge` to delete it. Back up sites with `bench backup` before
removing anything.

---

## 8. PostgreSQL variant — `vypgbench`

`vypgbench` is the PostgreSQL build of the same stack, tracking Frappe and
ERPNext `develop` rather than the stable v16 branch.

```bash
sudo snap install vypgbench
```

```bash
# Optional alias
sudo snap alias vypgbench.bench bench

# Add yourself to the snap_daemon group
sudo usermod -aG snap_daemon $USER && newgrp snap_daemon
```

Create a site — the PostgreSQL root password is generated at first start and
recorded automatically in `common_site_config.json`:

```bash
cd /var/snap/vypgbench/common/bench
vypgbench.bench new-site mysite.localhost --admin-password admin
```

Service and config commands mirror `vybench`, with the prefix swapped:

```bash
snap services vypgbench
sudo snap set vypgbench mode=developer
sudo snap set vypgbench nginx=true
sudo snap logs vypgbench.web -f
```

Client tools:

```bash
vypgbench.psql -U postgres -h 127.0.0.1
vypgbench.pg-dump -U postgres mydb > dump.sql
vypgbench.pg-restore -U postgres -d mydb dump.sql
```

Data lives in `/var/snap/vypgbench/common/` with the same layout as `vybench`
(`bench/sites/`, `postgres/`, `run/`, `bench/logs/`).
