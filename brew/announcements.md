Hi everyone,

Setting up Frappe Bench and ERPNext has always been a rite of passage for many in our community. While Docker and manual bench setups are powerful, we often see developers and teams running into version mismatches (Python/Node), sluggish Docker I/O on macOS workstations, port collisions, or fragile setup scripts on clean servers.

Over the past few months, we've been working on **Vybench** — the universal developer and operational environment for Frappe and ERPNext, open for community preview.

### What's New in Vybench

- 🖥️ **Interactive Terminal UI (`vybench tui`)**: Full-screen console with service status and controls, site management, log tailing, bench switching, and an app marketplace.
- 🔀 **Multi-Bench Orchestration (`vybench bench`)**: Create, attach, and switch between isolated benches on one machine (`vybench bench switch <name>`). An existing install becomes the `default` bench.
- 🎯 **More Than One Frappe Version**: A bench on the packaged Frappe release is ready in seconds. Frappe `15`, `develop`, or any tag or branch is built with `bench init` (network access and a few minutes).
- 🗄️ **MariaDB or PostgreSQL**: Choose per bench and per site (`--db mariadb` or `--db postgres`). MariaDB 11.8 is bundled; PostgreSQL 16 comes from Homebrew's `postgresql@16` on macOS or the `vypgbench` snap on Linux.
- 📦 **FPM App Marketplace**: Browse the [fpm registry](https://fpm.vyogo.tech) and install prebuilt apps with no asset compilation. Today's packages are built for Linux x86_64; the TUI tells you when a package does not fit your machine.
- 🛡️ **Isolated Redis Queues**: Each bench's `bench_id` namespaces its queues, so jobs never cross benches.

We'd love for fellow developers and community members to try it out and share your thoughts!

---

### How to Try It

#### 1. On macOS (Homebrew Tap)

Native Apple Silicon & Intel support:

```bash
brew tap vyogotech/tap
brew install vybench

# Start MariaDB, Redis and the Frappe services
vybench start

# Launch interactive TUI
vybench tui

# Or use CLI
vybench bench new mybench --db mariadb
vybench bench switch mybench
vybench restart
vybench bench new-site mysite.localhost --admin-password admin
```

---

#### 2. On Any Linux Distro (Snap Store)

Strictly confined, self-contained, works on Ubuntu, Debian, Fedora, Arch, RHEL:

```bash
sudo snap install vybench

# Add user to snap_daemon group for non-root bench commands
sudo usermod -aG snap_daemon $USER && newgrp snap_daemon

# Launch interactive TUI
sudo vybench.tui

# Create and switch benches (benches live in /var/snap/vybench/common, hence sudo)
sudo vybench.bench bench new erp-v15 --version 15
sudo vybench.bench bench switch erp-v15
sudo snap restart vybench
```

---

#### 3. On Ubuntu (24.04 LTS) & Debian (12 Bookworm) — Native APT

Native `.deb` packages bundling Python 3.14 and integrating directly with your distribution's native `systemd`, `nginx`, and `mariadb`:

```bash
# For Ubuntu 24.04 (Noble):
echo "deb [trusted=yes] https://apt.vyogo.tech noble main" | sudo tee /etc/apt/sources.list.d/vybench.list

# For Debian 12 (Bookworm):
echo "deb [trusted=yes] https://apt.vyogo.tech bookworm main" | sudo tee /etc/apt/sources.list.d/vybench.list

sudo apt update
sudo apt install -y vybench
sudo vybench-setup --site dev.localhost --admin-password admin
```

---

### Why We Built This

- **Zero Host Pollution:** Self-contained runtime; host system libraries, Python, and Node versions are never touched.
- **Pure Native Speed:** Full bare-metal NVMe/SSD throughput — no Docker virtualization overhead on developer workstations.
- **Enterprise-Ready Topology:** Multi-queue background workers, SocketIO realtime server, Celery/RQ schedulers, and automatic crash recovery out-of-the-box.
- **Fast App Installation:** Install prebuilt FPM packages without a `yarn` build or `node_modules` (Linux x86_64 packages today).

---

### We'd Love Your Feedback

This is an open community preview. Our goal is to make Frappe & ERPNext development and deployment as frictionless and powerful as possible for everyone.

Please test it on your machines, report any edge cases, and let us know what improvements or additional features you'd like to see!

- Repository & Docs: https://github.com/vyogotech/vybench
- APT Repository: https://apt.vyogo.tech

For commercial support, custom cloud VM images, or enterprise deployments, feel free to reach out at **dev@vyogo.tech**.

Thank you for your time, and happy hacking!