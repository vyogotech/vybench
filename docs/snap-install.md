# Vybench Snap — Installation & User Guide

A self-contained Frappe & ERPNext orchestration stack for any Linux distribution with `snapd`
(Fedora, Ubuntu, Debian, Arch, RHEL). Python 3.14, MariaDB 11.8, Redis 7, Node.js 24, and
Nginx are bundled directly inside the snap, with an interactive Terminal UI (`vybench.tui`),
multi-bench management, and FPM app installation. Each bench and site chooses MariaDB
(bundled) or PostgreSQL 16 (installed separately on the host, reachable on
127.0.0.1:5432) — the same model as Homebrew's optional `postgresql@16` on macOS. There is
no separate PostgreSQL-flavoured snap.

Nothing is taken from the host, ensuring zero library conflicts and zero host OS pollution.

---

## 1. Install

```bash
sudo snap install vybench
```

On a DigitalOcean Droplet, site data can live on a Block Storage volume. See [Droplet storage](droplet-storage.md).

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

The install starts MariaDB and Redis, generates a random database root password, and brings
up the full application stack. No post-install step is required to get a working server.
A PostgreSQL-engine bench needs a PostgreSQL 16 server installed separately on the same
host (see §5) — nothing extra to install for MariaDB benches.

---

## 2. CLI Aliasing & Group Access

The snap exposes commands under the `vybench` namespace. Alias them for standard convenience:

```bash
sudo snap alias vybench.bench bench
sudo snap alias vybench.fpm fpm
sudo snap alias vybench.tui vybench-tui
```

Check with `snap aliases vybench`; undo with `sudo snap unalias bench`.

### Granting Access (No `sudo` Required)

The background services run as the unprivileged `snap_daemon` account rather than root.
To run `bench` and `vybench` as your normal host user without `sudo`:

```bash
sudo usermod -aG snap_daemon $USER
newgrp snap_daemon          # or log out and back in
```

This mirrors Docker's `usermod -aG docker $USER`. Membership grants read/write access
to benches and database sockets.

---

## 3. Interactive Terminal UI (`vybench.tui`)

Launch the full-screen terminal console:

```bash
sudo vybench.tui
# Or: sudo vybench.bench   (with no arguments, in a terminal)
```

It needs `sudo` because benches, the registry and the `current-bench` symlink live in
`/var/snap/vybench/common`, which belongs to root. Bench commands it runs still drop to
`snap_daemon`, as `sudo vybench.bench` always does.

The TUI provides 5 views:
1. **[1] Overview**: Status of the snap's services from `snapctl`, with `[s]` start all,
   `[x]` stop all and `[r]` restart all. Stop and restart leave MariaDB and Redis running.
2. **[2] Sites**: The active bench's sites. `[n]` creates one on any bench, `[o]` opens it in a browser,
   `[B]` backs it up, `[R]` restores a backup, and `[d]` drops it after you type its name. A site left
   over from a failed install is detected and replaced when you create it again.
3. **[3] Marketplace**: Packages from fpm.vyogo.tech with category filters, search (`[/]`),
   an inspector, and `[i]` to install with the bundled `fpm`. `[t]` also installs onto a site.
4. **[4] Logs**: Tails the active bench's `logs/*.log`. Service output goes to the journal:
   `sudo snap logs -f vybench`.
5. **[5] Benches**: `[Enter]` switches, `[n]` creates, `[a]` attaches, `[d]` removes from the registry.

Global hotkeys: `[Tab]` cycles tabs, `[1]`-`[5]` jump to tabs, `[b]` opens Benches, `[q]` quits.

---

## 4. Multi-Bench Management

Run these with `sudo`, for the reason above:

```bash
# List all registered benches (* marks the active bench)
sudo vybench.bench bench list

# Display the active bench
sudo vybench.bench bench current

# Switch the active bench, then restart the services so they serve it
sudo vybench.bench bench switch <name>
sudo snap restart vybench

# Create a bench (the packaged Frappe release is linked in seconds)
sudo vybench.bench bench new <name> [--version VER]

# Register an existing bench, or remove one from the registry (files are kept)
sudo vybench.bench bench attach <name> <path>
sudo vybench.bench bench drop <name>
```

### Frappe Versions

Without `--version`, a new bench links the Frappe release inside the snap and is ready at
once. Any other version is built with `bench init`, which clones Frappe and installs its
Python and Node dependencies. That needs network access, takes several minutes, and uses the
bundled Python 3.14, which an older Frappe release may not support.

```bash
# Frappe 15, built with bench init --frappe-branch version-15
sudo vybench.bench bench new erp-v15 --version 15

# The develop branch
sudo vybench.bench bench new erp-dev --version develop
```

`vybench` detects versions from `__init__.py`, the git branch, `common_site_config.json`,
and Python metadata. Each bench has its own `bench_id`, which namespaces its Redis queues so
jobs from different benches never mix.

---

## 5. Databases (MariaDB or PostgreSQL)

The `vybench` snap bundles **MariaDB 11.8**. Each bench and site independently chooses
MariaDB or PostgreSQL — there is no separate PostgreSQL-flavoured snap. A PostgreSQL bench
talks to a PostgreSQL 16 server on the same host, reachable on `127.0.0.1:5432`, installed
however you prefer (a distro package, a container, or your own build).

### Creating Sites

```bash
# A MariaDB site (the bundled server, over its own UNIX socket)
bench new-site maria.localhost --db-type mariadb --admin-password admin

# A PostgreSQL site (talks to a server on 127.0.0.1:5432)
bench new-site pg.localhost --db-type postgres --admin-password admin \
  --db-root-username postgres --db-root-password <postgres-root-password>
```

### Direct Database Client Tools

```bash
# MariaDB client (bundled)
vybench.mysql -u root -p -S /var/snap/vybench/common/run/mysql.sock

# PostgreSQL client: use the one installed alongside your PostgreSQL server, e.g.
psql -U postgres -h 127.0.0.1
pg_dump -U postgres mydb > dump.sql
pg_restore -U postgres -d mydb dump.sql
```

---

## 6. Frappe Package Manager (FPM) Integration

`vybench` bundles the Frappe Package Manager CLI and live TUI marketplace. FPM packages ship
pre-compiled assets, eliminating the need for `yarn`, `node_modules`, or heavy `bench build` steps:

```bash
# Search and browse apps
vybench.fpm search hrms

# Install pre-compiled package into active bench
vybench.fpm install frappe/hrms==15.63.3

# Install app onto site
bench --site dev.localhost install-app hrms
```

---

## 7. Choose an Install Mode

```bash
sudo snap set vybench mode=production   # default
sudo snap set vybench mode=developer
```

|                         | production                            | developer                        |
| ----------------------- | ------------------------------------- | -------------------------------- |
| MariaDB (or PostgreSQL), Redis | running                        | running                          |
| web, workers, scheduler, socketio | running as managed services | **off** — you run `bench serve`  |
| `apps/`, `env/`         | read-only, shared from the snap       | real writable copy (~1.15 GB)    |
| Upgrade with            | `snap refresh` (`snap revert` to roll back) | `bench update`, `bench switch-to-branch` |
| Best for                | servers, demos, CI                    | app development                  |

Switching to `developer` copies `apps/` and `env/` out of the read-only snap into the bench so
`get-app`, `new-app`, source edits, `pip install`, `build` and `watch` all work.

---

## 8. Optional Services & Configuration

```bash
sudo snap set vybench nginx=true          # reverse proxy, production only
sudo snap set vybench nginx-port=8080     # default 8080
sudo snap set vybench watch=true          # asset rebuilder, developer only
sudo snap set vybench bind=0.0.0.0        # expose the web port on the LAN
```

---

## 9. Everyday Service Commands

```bash
snap services vybench                     # what is running
sudo snap restart vybench.web             # restart web tier
sudo snap restart vybench                 # restart all services
sudo snap logs vybench.web -f             # follow live web logs
sudo snap get vybench mode                # check current mode
```

### Data Storage & Paths

All runtime and persistent data lives in `/var/snap/vybench/common`:

| Path | Contents |
| :--- | :--- |
| `benches/<name>/` | Bench root for bench `<name>` (`sites/`, `apps/`, `env/`) |
| `current-bench` | Symlink pointing to the currently active bench |
| `bench/` | Default single-bench root (fully backward compatible) |
| `mariadb/` | MariaDB database storage directory |
| `run/mysql.sock` | MariaDB UNIX domain socket |
| `bench/logs/` | Frappe's own logs for the default bench (each bench has its own `logs/`) |
| `benches.json` | Registry of benches, next to `current-bench` |

`snap remove vybench` keeps this directory as a snapshot; use
`snap remove --purge` to delete it. Back up sites with `bench backup` before
removing anything.
