# vybench

**Frappe Bench v16 & ERPNext v16 — Linux & macOS**

`vybench` packages Frappe v16, ERPNext v16, Python 3.14, Node.js 24, Redis 7, MariaDB, Nginx, and `wkhtmltopdf` into a portable, single-command package.

Works out-of-the-box on **Ubuntu**, **Debian**, **Fedora**, **Arch**, **RHEL**, and any Linux distribution running `snapd`, and on **macOS** (Apple Silicon & Intel) via Homebrew.

---

## Quick Start

### macOS (Homebrew)

```bash
# Add the vybench tap
brew tap vyogotech/tap

# Install vybench (builds from source — takes ~10 min on first install)
brew install vybench

# Start datastores (registered to auto-start at login)
brew services start mariadb
brew services start redis

# Start the Frappe application tier
brew services start vybench

# Create your first site
vybench bench new-site mysite.localhost --admin-password admin
echo "127.0.0.1 mysite.localhost" | sudo tee -a /etc/hosts
# → Open http://mysite.localhost:8000
```

Uninstall:
```bash
brew services stop vybench
brew uninstall vybench
```

### Linux (Snap)

Install from the Snap Store:
```bash
sudo snap install vybench
```

Or install a locally built `.snap` package:
```bash
sudo snap install --dangerous vybench_16.0.0_amd64.snap
```

Remove the package and purge all associated services and runtime data:
```bash
sudo snap remove --purge vybench
```

## Operating Modes

`vybench` supports two operational modes: **Production** and **Developer**.

### 1. Production Mode (Default)
In production mode, systemd daemons (`web`, `worker`, `mariadb`, `redis`, `scheduler`, `socketio`) run continuously in the background as the unprivileged `snap_daemon` account. The codebase remains read-only for maximum stability.

```bash
sudo snap set vybench mode=production
```

### 2. Developer Mode
In developer mode, managed app services are disabled and the `apps/` and `env/` virtual environments are materialised into `$SNAP_COMMON/bench` as real, writable directories. This enables live code editing, `git pull`, `pip install`, `bench get-app`, `bench new-app`, and `bench build`.

```bash
sudo snap set vybench mode=developer
```

---

## User Permissions & CLI Usage

### Granting Access (No `sudo` Required)
To run `vybench.bench` as your normal host user without `sudo`, add your user account to the `snap_daemon` group:

```bash
sudo usermod -aG snap_daemon $USER
newgrp snap_daemon
```

### Managing Sites & Apps
```bash
# Create a new site
vybench.bench new-site site1.localhost --admin-password admin

# Fetch a custom app (Developer Mode)
vybench.bench get-app hrms --branch version-16

# Install an app onto a site
vybench.bench --site site1.localhost install-app hrms

# List installed apps
vybench.bench --site site1.localhost list-apps

# Build frontend assets
vybench.bench --site site1.localhost build
```

---

## Service Controls & Diagnostics

### macOS (Homebrew)

```bash
# Check service status
vybench status

# Restart all Frappe services
brew services restart vybench

# Tail application logs
vybench logs web           # gunicorn / bench serve
vybench logs worker-default
vybench logs scheduler
vybench logs socketio
```

### Linux (Snap)

```bash
# Check service status
sudo snap services vybench

# Restart all vybench services
sudo snap restart vybench

# Restart a specific service (e.g. gunicorn web server)
sudo snap restart vybench.web

# Tail live application logs
sudo snap logs -f vybench.web
```

---

## Building & Testing

### macOS — Homebrew Formula
```bash
# Install from source (first install, ~10 min)
make brew

# Audit formula style and correctness
make brew-audit

# Run formula test block
make brew-test
```

### Linux — Local Snap Build
To build the snap package locally using `snapcraft`:

```bash
# Build the snap package
snapcraft

# Or build in destructive-mode inside a dedicated VM / container
snapcraft --destructive-mode
```

### End-to-End Integration Testing
Run the complete automated integration test suite against a built `.snap` package:

```bash
./scripts/test-snap-package.sh dist/vybench_16.0.0_amd64.snap
```

The test runner automatically verifies:
1. Package installation & initial production service startup.
2. Mode switching to `developer`.
3. Site creation (`new-site`).
4. App cloning & installation (`get-app` & `install-app hrms`).
5. Mode switching back to `production`.
6. HTTP endpoint health checks (`http://127.0.0.1:8000/`).
7. Package removal & cleanup (`snap remove --purge`).

---

## CI/CD Pipeline

Continuous Integration is managed via `.github/workflows/ci.yml`:
* **Fast Tier (~1 min):** Validates shell syntax, YAML schemas, JSON configs, and Nginx template drift.
* **Slow Tier:** Automatically builds packages and runs full integration tests on **Ubuntu 24.04** and **Debian 12**.

---

## Licensing & Terms

* **vybench Packaging & Orchestration Tools:** Copyright (c) 2026 Vyogo Technologies. All Rights Reserved.  
  *Source-Available License (No Derivatives):* Source code is publicly viewable for inspection and verification. Inspection and evaluation are permitted; modification, creation of derivative works, re-branding, or hosting modified forks without prior written authorization from Vyogo Technologies is strictly prohibited. See [LICENSE](LICENSE) for details.
* **Frappe Framework:** MIT License
* **ERPNext:** GPL-3.0 License (shipped as unmodified upstream source inside `apps/erpnext`)
