# Vybench Developer Cheat Sheet

> Frappe®, ERPNext® and the Frappe logo are trademarks of Frappe Technologies Pvt. Ltd.
> Vybench is from Vyogo Technologies and is not affiliated with Frappe Technologies.

---

## Step 0 — Install & Set Up Access (Do This Once)

### macOS (Homebrew)
```bash
brew tap vyogotech/tap
brew install vybench

# Start vybench, with its own MariaDB (port 13306) and Redis (port 16379).
# Homebrew's mariadb and redis services are not used and can keep running.
vybench start
```

### Linux (Snap)
```bash
# Install stable
sudo snap install vybench

# Add yourself to the snap_daemon group so bench works without sudo
sudo usermod -aG snap_daemon $USER
newgrp snap_daemon                        # apply immediately (or log out/in)

# Alias for convenience
sudo snap alias vybench.bench bench
```

After this, all `bench` commands below run **without `sudo`**.

---

## Interactive Terminal UI (`vybench tui`)

Launch the full-screen terminal interface:

```bash
vybench tui                               # or plain `vybench` in a terminal
vybench.tui                               # on the snap
vybench-tui --bench-path /path/to/bench   # open it on another bench
```

### Global Keyboard Navigation

| Key | Action |
| :--- | :--- |
| `[1]` – `[5]` | Jump to Overview, Sites, Marketplace, Logs, Benches |
| `[Tab]` / `[Shift+Tab]` | Next / previous tab |
| `[b]` | Jump to Benches |
| `[q]` / `Ctrl+C` | Quit (Ctrl+C cancels a running job first) |

Long-running actions stream their output in a window: `[x]` cancels, `[↑↓]`/`[PgUp]`/`[PgDn]` scroll, and `[Esc]` closes it once it is done. Errors and confirmations appear in the status bar at the bottom.

### View-Specific Hotkeys

- **[1] Overview:**
  - `[s]` start all, `[x]` stop all, `[r]` restart all (stop and restart leave the databases running)
  - `[u]` refresh now (status also refreshes every 5 seconds while the tab is open)
- **[2] Sites:**
  - `[↑]` / `[↓]` (or `k` / `j`) select a site
  - `[n]` new site: bench (the active one by default, `[←]`/`[→]` for another), domain, administrator password, and database. PostgreSQL also asks for its superuser.
    - The **Server** line shows whether the bench's database is running; `Ctrl+S` starts it.
    - If the name is taken, the site is checked. One left over from a failed install is replaced in place; a working one needs **Force** (`Space` to tick) and is backed up first.
  - `[o]` open the site in a browser, on the bench's `webserver_port`
  - `[B]` back up the site with its files (`bench --site <site> backup --with-files`)
  - `[R]` restore: pick a backup of the site (`[←]`/`[→]`) or another `.sql.gz`, into this site or a new name. Options: restore files, back up first, and **Force** for a backup from an older Frappe.
  - `[d]` drop the site; you type its name to confirm, and bench backs it up first. **Force** carries on if that backup fails; a half-made site with no database is simply archived.
  - A `⚠` in the list marks a site whose install did not finish.
- **[3] Marketplace:**
  - `[←]` / `[→]` (or `h` / `l`) cycle categories
  - `[/]` search (`[Enter]` keeps the query, `[Esc]` clears it)
  - `[t]` choose a site to install onto as well as the bench
  - `[i]` install with `fpm install`; `[r]` reload the catalog; `Ctrl+U` / `Ctrl+D` scroll the inspector
- **[4] Logs:**
  - `[←]` / `[→]` switch between the `.log` files found
  - `[f]` follow on/off; `[g]` / `[G]` top / bottom; `[/]` filter
- **[5] Benches:**
  - `[Enter]` switch to the selected bench
  - `[n]` new bench: name, database, and Frappe version. The form says whether the version is linked from the package or built with `bench init`.
  - `[a]` attach an existing bench directory; `[d]` remove a bench from the registry

---

## Multi-Bench Management

`vybench bench` manages benches (on the snap: `sudo vybench.bench bench …`):

```bash
vybench bench list                        # * marks the active bench
vybench bench current                     # active bench, and why it is active
vybench bench switch <name>               # repoint current-bench for every shell and the services
vybench bench new <name> [--db mariadb|postgres] [--version VER] [--python PATH] [--switch=false]
vybench bench attach <name> <path>        # register an existing bench directory
vybench bench drop <name>                 # remove from the registry; files are kept
```

After a switch, restart the services so they serve the new bench: `vybench restart`, or `sudo snap restart vybench`.

### Frappe Versions

```bash
# The packaged release: apps/ and env/ are linked in, ready in seconds
vybench bench new erp

# Anything else is built with bench init (network access, several minutes)
vybench bench new erp-v15 --version 15        # bench init --frappe-branch version-15
vybench bench new erp-dev --version develop
vybench bench new erp-tag --version v16.3.0   # a release tag

vybench bench list
#    NAME     DB       FRAPPE        SITES  PATH
#    default  mariadb  v16.34.1      1      /opt/homebrew/var/vybench/bench
#  * erp      mariadb  v16.34.1      0      /opt/homebrew/var/vybench/benches/erp
#    erp-v15  mariadb  v15.80.0      0      /opt/homebrew/var/vybench/benches/erp-v15
```

`bench init` uses the bundled Python 3.14 unless you pass `--python`; an older Frappe release may need an older Python. A failed or cancelled build removes its half-built directory.

`vybench` detects versions from:
1. `apps/frappe/frappe/__init__.py` (`__version__ = "..."`)
2. `apps/frappe/.git` active branch
3. `sites/common_site_config.json` (`"frappe_version"`)
4. Python environment `dist-info/METADATA`

---

## Database Engines (MariaDB & PostgreSQL)

Each bench and site chooses its engine. MariaDB ships with vybench. PostgreSQL needs a PostgreSQL 16 server on `127.0.0.1:5432`: `brew install postgresql@16 && brew services start postgresql@16` on macOS, or the separate `vypgbench` snap on Linux.

```bash
# MariaDB site (vybench supplies the root credentials)
vybench bench new-site site-maria.localhost --db-type mariadb --admin-password admin

# PostgreSQL site (give the PostgreSQL superuser)
vybench bench new-site site-pg.localhost --db-type postgres --admin-password admin \
  --db-root-username postgres --db-root-password <password>
```

### Direct Database Client Access

```bash
# MariaDB client
vybench mysql
# Or on Snap:
vybench.mysql -u root -S /var/snap/vybench/common/run/mysql.sock

# PostgreSQL client
psql -h 127.0.0.1 -U postgres             # Homebrew postgresql@16
vypgbench.psql -U postgres -h 127.0.0.1   # the vypgbench snap
```

---

## Service Management

### macOS (Homebrew)
```bash
vybench status                            # check status of all services
vybench start                             # start all services
vybench stop                              # stop all services
vybench restart                           # restart all services
vybench logs web                          # tail web logs
```

### Linux (Snap)
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
| `vybench.mariadb` | MariaDB database daemon |
| `vypgbench.postgres` | PostgreSQL database daemon (the `vypgbench` snap, instead of MariaDB) |
| `vybench.redis` | Cache + queue |
| `vybench.web` | Gunicorn (production) / Bench serve (developer) |
| `vybench.worker` | Background jobs (default queue) |
| `vybench.worker-short` | Background jobs (short queue) |
| `vybench.worker-long` | Background jobs (long queue) |
| `vybench.scheduler` | Cron / scheduled tasks |
| `vybench.socketio` | Realtime / websockets |
| `vybench.nginx` | Reverse proxy (opt-in) |
| `vybench.watch` | Asset rebuilder (developer only) |

---

## Operating Modes (Snap)

```bash
sudo snap set vybench mode=production     # default — managed stack, read-only apps/
sudo snap set vybench mode=developer      # writable apps/, live code editing
sudo snap get vybench mode                # check current mode
```

| | Production | Developer |
| :--- | :--- | :--- |
| `apps/`, `env/` | Read-only (squashfs) | Writable copy (~1 GB) |
| Web + workers | Managed by snapd | Run via supervisor or `bench serve` |
| Upgrade path | `snap refresh` | `bench update` |
| Custom apps | ❌ | ✅ |

---

## Useful Bench Commands

```bash
# Sites
vybench.bench new-site <name> --admin-password <pw>
vybench.bench drop-site <name>
vybench.bench list-sites
vybench.bench --site <name> migrate
vybench.bench --site <name> clear-cache
vybench.bench --site <name> backup

# Apps via Frappe Package Manager (Instant, no build step)
fpm install frappe/hrms==15.63.3
vybench.bench --site <name> install-app hrms

# Apps via Git (Developer Mode)
vybench.bench get-app <app-name-or-url>
vybench.bench --site <name> install-app <app>
vybench.bench --site <name> uninstall-app <app>
vybench.bench list-apps

# Developer tools
vybench.bench serve                       # dev server (developer mode)
vybench.bench start                       # start all processes (Procfile)
vybench.bench build --app <app>           # rebuild assets
vybench.bench update                      # pull + migrate + build all apps

# Console
vybench.bench --site <name> console       # Python REPL
vybench.bench --site <name> mariadb       # MariaDB shell for site
```

---

## Data Paths

| Path | Contents |
| :--- | :--- |
| `$VYBENCH_VAR/benches/<name>/sites/` | Sites, configs, uploaded files for bench `<name>` |
| `$VYBENCH_VAR/current-bench` | Symlink pointing to active bench directory |
| `~/.config/vybench/benches.json` | Registry manifest tracking all local benches |
| `$VYBENCH_VAR/mariadb/` | MariaDB database data directory |
| `$VYBENCH_VAR/run/mysql.sock` | MariaDB UNIX domain socket |
| `$VYBENCH_LOG/` | Service logs (`web`, `worker`, `scheduler`, etc.) |

---

## Links

- **Repository & Docs:** https://github.com/vyogotech/vybench
- **Issues & Support:** https://github.com/vyogotech/vybench/issues
- **FPM Registry:** https://fpm.vyogo.tech
- **Frappe Framework Docs:** https://frappeframework.com/docs
