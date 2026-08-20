# Vybench Developer Cheat Sheet

> Frappe®, ERPNext® and the Frappe logo are trademarks of Frappe Technologies Pvt. Ltd.
> Vybench is an independent distribution and is not affiliated with Frappe Technologies.

---

## Step 0 — Install & Set Up Access (Do This Once)

```bash
# MariaDB v16 (stable)
sudo snap install --edge vybench          # edge (latest)
sudo snap install vybench                 # stable

# PostgreSQL / develop  (vypgbench)
sudo snap install vypgbench

# Add yourself to the snap_daemon group so bench works without sudo
sudo usermod -aG snap_daemon $USER
newgrp snap_daemon                        # apply immediately (or log out/in)

# Alias for convenience (use the snap you installed)
sudo snap alias vybench.bench bench       # MariaDB build
# sudo snap alias vypgbench.bench bench  # PostgreSQL build
```

After this, all `bench` commands below run **without `sudo`**.

---

## Service Management

```bash
snap services vybench                     # status of all services
sudo snap start vybench.web               # start a service
sudo snap stop vybench.web                # stop a service
sudo snap restart vybench.web             # restart a service
sudo snap logs vybench.web -f             # follow logs
sudo snap logs vybench.mariadb -n 50      # last 50 lines
```

| Service | Role |
| :--- | :--- |
| `vybench.mariadb` | Database |
| `vybench.redis` | Cache + queue |
| `vybench.web` | Gunicorn (HTTP) |
| `vybench.worker` | Background jobs (default queue) |
| `vybench.worker-short` | Background jobs (short queue) |
| `vybench.worker-long` | Background jobs (long queue) |
| `vybench.scheduler` | Cron / scheduled tasks |
| `vybench.socketio` | Realtime / websockets |
| `vybench.nginx` | Reverse proxy (opt-in) |
| `vybench.watch` | Asset rebuilder (developer only) |

---

## Modes

```bash
sudo snap set vybench mode=production     # default — managed stack, read-only apps/
sudo snap set vybench mode=developer      # writable apps/, you run bench serve
sudo snap get vybench mode                # check current mode
```

| | Production | Developer |
| :--- | :--- | :--- |
| `apps/`, `env/` | Read-only (squashfs) | Writable copy (~1 GB) |
| Web + workers | Managed by snapd | You run `bench start` / `bench serve` |
| Upgrade path | `snap refresh` | `bench update` |
| Custom apps | ❌ | ✅ |

---

## Quick Start — First Site

```bash
cd /var/snap/vybench/common/bench
vybench.bench new-site mysite.localhost --admin-password admin

# Map hostname (optional)
echo "127.0.0.1 mysite.localhost" | sudo tee -a /etc/hosts

# Open in browser
# http://mysite.localhost:8000  →  login: Administrator / admin
```

---

## Installing Frappe Apps

```bash
# 1. Switch to developer mode (one-time, ~1 min, ~1 GB)
sudo snap set vybench mode=developer

# 2. Install app from GitHub
vybench.bench get-app erpnext
vybench.bench get-app https://github.com/your-org/your-app

# 3. Install app on a site
vybench.bench --site mysite.localhost install-app erpnext

# 4. Build assets
vybench.bench --site mysite.localhost build

# 5. Switch back to production (apps are preserved)
sudo snap set vybench mode=production
```

> ⚠️ After installing custom apps, use `bench update` to upgrade — not `snap refresh`.
> `snap refresh` is safe and will not delete your apps, but may cause a frappe
> version mismatch until you run `bench update`.

---

## Upgrades

```bash
# Production mode (no custom apps)
sudo snap refresh vybench                 # atomic, snapd-managed

# Developer mode (custom apps installed)
vybench.bench update                      # updates frappe + all apps + assets
```

---

## Configuration

```bash
sudo snap set vybench nginx=true          # enable reverse proxy
sudo snap set vybench nginx-port=8080     # nginx listen port (default 8080)
sudo snap set vybench bind=0.0.0.0        # expose web port on LAN
sudo snap set vybench watch=true          # enable asset watcher (developer only)
sudo snap get vybench                     # show all config values
```

---

## Client Tools

```bash
# MariaDB
vybench.mysql -u root -S /var/snap/vybench/common/run/mysql.sock
vybench.mysqldump -u root -S /var/snap/vybench/common/run/mysql.sock mydb > dump.sql

# Redis
vybench.redis-cli ping
vybench.redis-cli monitor
```

---

## Data Paths

| Path | Contents |
| :--- | :--- |
| `/var/snap/vybench/common/bench/sites/` | Sites, configs, uploaded files |
| `/var/snap/vybench/common/bench/logs/` | Bench + worker logs |
| `/var/snap/vybench/common/mariadb/` | Database files |
| `/var/snap/vybench/common/run/mysql.sock` | MariaDB socket |
| `/var/snap/vybench/common/bench/sites/common_site_config.json` | Shared config + DB credentials |

---

## Useful Bench Commands

> All commands below assume you've added yourself to `snap_daemon` (Step 0).
> For `snap set` / `snap refresh` you still need `sudo` — those are system-level.

```bash
# Sites
vybench.bench new-site <name> --admin-password <pw>
vybench.bench drop-site <name>
vybench.bench list-sites
vybench.bench --site <name> migrate
vybench.bench --site <name> clear-cache
vybench.bench --site <name> backup

# Apps
vybench.bench get-app <app-name-or-url>
vybench.bench --site <name> install-app <app>
vybench.bench --site <name> uninstall-app <app>
vybench.bench list-apps

# Dev
vybench.bench serve                       # dev server (developer mode)
vybench.bench start                       # start all processes (Procfile)
vybench.bench build --app <app>           # rebuild assets
vybench.bench update                      # pull + migrate + build all apps

# Console
vybench.bench --site <name> console       # Python REPL
vybench.bench --site <name> mariadb       # MariaDB shell for site
```

---

## Rollback

```bash
snap revert vybench                       # revert to previous snap revision (production only)
snap list --all vybench                   # list all installed revisions
```

---

## Remove

```bash
snap remove vybench                       # removes snap, keeps data snapshot
snap remove --purge vybench              # removes snap + all data (irreversible)
```

---

## Links

- **Docs:** https://github.com/vyogotech/vybench
- **Issues:** https://github.com/vyogotech/vybench/issues
- **Snap Store (MariaDB):** https://snapcraft.io/vybench
- **Snap Store (PostgreSQL):** https://snapcraft.io/vypgbench
- **Frappe Docs:** https://frappeframework.com/docs
- **Bench Docs:** https://frappeframework.com/docs/user/en/bench

---

## `vypgbench` — PostgreSQL / develop variant

All commands are identical to `vybench`; swap the prefix.

```bash
# Services
snap services vypgbench
sudo snap start  vypgbench.web
sudo snap logs   vypgbench.web -f
sudo snap logs   vypgbench.postgres -n 50

# Mode
sudo snap set vypgbench mode=developer
sudo snap get vypgbench mode

# Sites
cd /var/snap/vypgbench/common/bench
vypgbench.bench new-site mysite.localhost --admin-password admin

# PostgreSQL client
vypgbench.psql -U postgres -h 127.0.0.1
vypgbench.pg-dump -U postgres mydb > dump.sql
```

| Service | Role |
| :--- | :--- |
| `vypgbench.postgres` | Database |
| `vypgbench.redis` | Cache + queue |
| `vypgbench.web` | Gunicorn (HTTP) |
| `vypgbench.worker` | Background jobs (default queue) |
| `vypgbench.worker-short` | Background jobs (short queue) |
| `vypgbench.worker-long` | Background jobs (long queue) |
| `vypgbench.scheduler` | Cron / scheduled tasks |
| `vypgbench.socketio` | Realtime / websockets |

Data paths: `/var/snap/vypgbench/common/{bench/sites, postgres, run, bench/logs}`
