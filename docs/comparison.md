# Architectural Comparison: Vybench vs. Host Scripts vs. Docker

When setting up Frappe and ERPNext, developers and DevOps engineers often struggle with the trade-offs between **host-level provisioning scripts** (e.g., `flexcomng/erpnext_quick_install`) and **Docker-based orchestrators** (e.g., `rtcamp/frappe-manager`).

**Vybench** is the **universal tooling designed to transform the Frappe developer and operational experience**. It strictly adheres to the official Frappe installation specification and production runtime topology (multi-queue workers, gunicorn/nginx, socketio, redis caches/queues, MariaDB configurations), while delivering a **highly secure, flexible, and zero-pollution environment** on any operating system without the host clobbering of shell scripts or the heavyweight overhead and filesystem latency of Docker.

---

## 1. High-Level Comparison Matrix

| Feature / Metric | **Host Scripts** (`flexcomng/erpnext_quick_install`) | **Docker CLI** (`rtcamp/frappe-manager`) | **Vybench** (Snap / Brew) |
| :--- | :--- | :--- | :--- |
| **Delivery Model** | Monolithic Bash script executing `apt-get`, `pip`, `npm` | Python CLI (`fm`) driving Docker Compose containers | Pre-packaged, strictly confined Snap (`core24`) & macOS Homebrew formula |
| **Host OS Impact** | 🔴 **High Pollution** (Modifies `/etc`, global Python, Node, MariaDB, Redis, Nginx) | 🟡 **Moderate** (Requires Docker engine/daemon, creates Docker bridges, storage pools) | 🟢 **Zero Pollution** (Completely self-contained; squashfs image + `$SNAP_COMMON`) |
| **OS & Distro Support** | 🔴 **Debian/Ubuntu Only** (Hardcoded version checks; rejects Fedora/Arch/macOS) | 🟢 **Cross-platform** (Wherever Docker Desktop / Engine runs) | 🟢 **Universal Linux & macOS** (Fedora, Arch, Ubuntu, Debian, RHEL, openSUSE, macOS) |
| **Performance & I/O** | 🟢 **Native** | 🔴 **Degraded on macOS/Windows** (Bind-mount latency for `node_modules` & `apps/`) | 🟢 **Native Linux / macOS Speed** (Zero VM layer, direct kernel execution) |
| **Resource Overhead** | 🟢 Low (Runs native processes) | 🔴 **Heavy** (Docker daemon + VM on Mac/Win + multi-container RAM overhead) | 🟢 **Minimal** (Single unprivileged daemon set; no container runtime overhead) |
| **Uninstallation & Cleanup** | 🔴 **Messy / Manual** (Scatters files in `/var`, `/etc`, `/home`, lingering daemons) | 🟡 **Docker Prunes Required** (Leaves images, dangling volumes, network interfaces) | 🟢 **Atomic Single-Command Purge** (`sudo snap remove --purge vybench`) |
| **Security & Confinement** | 🔴 **None** (Runs as root/sudo, unconfined host access) | 🟡 **Container Isolation** (Docker daemon socket security considerations) | 🟢 **Strict Sandbox** (AppArmor, seccomp, non-root `snap_daemon` system user) |
| **Developer Experience** | 🟡 Standard `bench` on host, but fragile to system upgrades | 🟡 Wrapped inside `fm` CLI / container subshells (`docker exec`) | 🟢 Direct native `bench` CLI (`bench new-site`, `bench get-app`, etc.) |
| **Dev ↔ Prod Switching** | 🔴 Manual reconfiguration of Supervisor / Nginx configs | 🟡 Docker Compose configuration swapping | 🟢 **Declarative 1-Command Toggle** (`snap set vybench mode=developer`) |

---

## 2. In-Depth Architectural Comparison

### A. Host Provisioning Scripts (e.g. `flexcomng/erpnext_quick_install`)

Host installation scripts attempt to automate the manual Frappe setup guide. While convenient on a blank VM, they are destructive to everyday developer machines and shared servers.

#### Critical Drawbacks:
1. **Global System Pollution**: The script installs global `apt` packages, custom PPAs, global Python packages, Node.js/NVM, and overrides system configurations in `/etc/mysql/my.cnf`, `/etc/supervisor/`, and `/etc/nginx/`.
2. **Distro Lock-in**: Hardcoded checks enforce specific Ubuntu/Debian releases. If you run Fedora, Arch Linux, openSUSE, or macOS, the script refuses to run.
3. **Dependency Hell & Version Clashes**: If your machine already has MariaDB, Redis, or an existing Python/Node stack, the script risks overriding databases, conflicting on service ports, or clobbering pip libraries.
4. **No Clean Rollback or Uninstall**: There is no `uninstall.sh`. Removing the installation requires manually purging database files, stopping host supervisor units, editing web server configs, and hunting down scattered files.

---

### B. Docker-Based Orchestrators (e.g. `rtcamp/frappe-manager`)

`rtcamp/frappe-manager` (`fm`) solves host pollution by encapsulating the Frappe stack into multi-container Docker Compose environments. While excellent for standardized containers, Docker introduces significant friction for day-to-day development.

#### Critical Drawbacks:
1. **Docker Daemon Dependency & Licensing**: Requires a running Docker engine (or Docker Desktop, which carries licensing and resource constraints on corporate macOS/Windows setups).
2. **Filesystem Bind-Mount Latency**: On macOS and non-native storage drivers, mounting `apps/` and `node_modules` into containers incurs heavy virtualized I/O penalties. Tasks like `bench build`, `yarn install`, and site migrations run noticeably slower compared to native execution.
3. **Resource & Memory Footprint**: Running separate containers for Web, Worker, Redis-Cache, Redis-Queue, MariaDB, Mailpit, and Adminer consumes substantial memory overhead, CPU cycles, and disk space for layered container images and volumes.
4. **CLI Indirection**: Running bench commands often requires nested subshells (`fm subshell`) or `docker exec` wrappers rather than native terminal execution.

---

### C. The Vybench Solution (Strict Snap & Homebrew)

Vybench packages the entire Frappe v16 ecosystem (Python 3.14, Node.js 24, MariaDB 10.11, Redis 7, Nginx, and `wkhtmltopdf`) into a self-contained, native package.

#### Why Developers Choose Vybench:

#### 1. Zero Host Disruption
* **No `apt` or system library modifications**: Everything runs from an immutable, read-only package payload (`/snap/vybench`).
* **Isolated Datastores**: MariaDB and Redis communicate over isolated UNIX sockets in `$SNAP_COMMON/run/`. Your host MariaDB/Redis installations and ports remain untouched.

#### 2. Native Performance Without Virtualization
* **Direct OS Execution**: Vybench runs directly on the Linux kernel using native namespaces and cgroups (Snap) or native binaries (macOS Homebrew).
* **Zero I/O Penalty**: Asset building (`bench build`), Python bytecode loading, and hot reloading happen at full native SSD speeds without Docker bind-mount synchronization overhead.

#### 3. Single Solution on Any Environment
* **Linux**: One package works identically across Ubuntu, Debian, Fedora, Arch Linux, RHEL, CentOS, and openSUSE via `snapd`.
* **macOS**: Native Apple Silicon and Intel support via Homebrew (`brew install vybench`).

#### 4. Instant Mode Switching (Production vs. Developer)
Vybench provides a first-class operational switch without re-provisioning:
* **Production Mode (Default)**: Background daemons (`web`, `worker`, `scheduler`, `redis`, `mariadb`) run managed by systemd under the unprivileged `snap_daemon` user with read-only code mounts.
  ```bash
  sudo snap set vybench mode=production
  ```
* **Developer Mode**: Disables background web/worker daemons and exposes `apps/` and `env/` as writable directories in `$SNAP_COMMON/bench` for live code editing, custom app creation, `bench get-app`, and `bench serve`.
  ```bash
  sudo snap set vybench mode=developer
  ```

#### 5. Clean Lifecycle & Single-Command Purge
Testing a new site or app? When you're done, completely wipe all services, data, and configurations with zero residue:
```bash
sudo snap remove --purge vybench
```

---

## 3. Summary & Recommended Usage Guide

* **Use Vybench (Snap / Homebrew):**
  * **On VPS & Cloud Servers:** Ideal for production and staging deployments where you want a robust, self-contained, and systemd-managed Frappe stack without the operational overhead, resource footprint, and configuration complexity of Docker.
  * **In Environments Where Containers Cannot Run:** Perfect for VPS providers without nested virtualization support, restricted hosting environments, edge devices, or systems where you cannot/prefer not to maintain a container runtime daemon.
  * **On Developer Workstations (Linux / macOS):** Delivers full native SSD I/O performance (no Docker bind-mount synchronization lag), zero host OS pollution, and direct native CLI workflows.

* **Use Frappista SNE Images (FOR NON-PROD):**
  * **In Container-Based Environments:** For teams and developers running Docker/Podman workflows, quick local sandboxing, automated testing, or CI/CD runner pipelines in **non-production** environments.

* **Host Provisioning Scripts (`erpnext_install.sh`):**
  * Generally discouraged due to irreversible OS pollution, lack of uninstallation/rollback mechanisms, distro lock-in, and frequent dependency collisions on real-world systems.

