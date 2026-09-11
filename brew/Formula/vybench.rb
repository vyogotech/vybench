# typed: false
# frozen_string_literal: true

# Vybench: Frappe Bench v16 & ERPNext v16 — Homebrew formula for macOS.
#
# Installs via a custom tap:
#   brew tap vyogotech/tap
#   brew install vybench
#
# Architecture:
#   - depends_on mariadb, redis, python@3.14, node, yarn (all managed by Homebrew)
#   - `brew install` runs `bench init` and `bench get-app erpnext` into libexec/
#   - `post_install` bootstraps writable data directories under var/vybench/
#   - `brew services start vybench` launches vybench-supervisor (Frappe app tier)
#   - Datastores run as their own separate brew services (mariadb, redis)
#
# Equivalent of the Linux snapcraft.yaml for macOS.
class Vybench < Formula
  desc "Frappe Bench v16 & ERPNext v16 single-node server stack for macOS"
  homepage "https://github.com/vyogotech/vybench"
  url "https://github.com/vyogotech/vybench/archive/refs/tags/v16.0.0.tar.gz"
  sha256 "9fd51b130187359c1d793857d868296968a9b1db87b63a4be0022e751ea77ca4"
  license "GPL-3.0-only"
  head "https://github.com/vyogotech/vybench.git", branch: "main"

  # ── Runtime dependencies ──────────────────────────────────────────────────
  # Datastores and runtimes managed by Homebrew; vybench only adds the
  # Frappe application layer on top of them.
  depends_on "go" => :build
  depends_on :macos
  depends_on "mariadb"
  depends_on "node"
  depends_on "python@3.14"
  depends_on "redis"
  depends_on "yarn"
  # PostgreSQL benches and sites need a server on 127.0.0.1:5432; it is not
  # started for you: brew services start postgresql@16
  depends_on "postgresql@16" => :optional

  # wkhtmltopdf with patched Qt is required for PDF generation.
  # The upstream Homebrew formula was removed; users must install via Cask:
  #   brew install --cask wkhtmltopdf
  # See caveats below.

  bottle do
    root_url "https://github.com/vyogotech/homebrew-tap/releases/download/v16.0.0"
    rebuild 1
    sha256 cellar: :any, arm64_tahoe: "807c33ded1a08bf65b1d810074e2a17398939ef3feabf52e98bfbcf55fcee6af"
  end

  # ── Build-time resources ──────────────────────────────────────────────────
  # frappe-bench CLI is needed only at build time to run `bench init`.
  # It is also installed into the venv so the runtime bench command works.
  resource "frappe-bench" do
    url "https://files.pythonhosted.org/packages/59/bd/21a8d447f9c6b6df651753fee40899def1004987a348131112b1d7470390/frappe_bench-5.31.0.tar.gz"
    sha256 "bcc829befe2fb6c5145cd6f1ebb6d5a8b34bcf58de99cb8a6afc0e88430776c5"
  end

  # fpm installs prebuilt Frappe apps into a bench; the TUI's marketplace
  # drives it. Same tag as the snap's fpm part.
  resource "fpm" do
    url "https://github.com/vyogotech/fpm/archive/refs/tags/v4.2.0.tar.gz"
    sha256 "262f746fb52b9502dc76707f0394aad20cf6396e51685a03e1864bb96abad1d6"
  end

  def install
    # ── Paths ──────────────────────────────────────────────────────────────
    python_bin = formula_opt_bin("python@3.14")/"python3.14"
    yarn_bin   = formula_opt_bin("yarn")/"yarn"

    bench_src = libexec/"frappe-bench"

    # ── Environment ────────────────────────────────────────────────────────
    ENV.prepend_path "PATH", formula_opt_bin("python@3.14")
    ENV.prepend_path "PATH", formula_opt_bin("node")
    ENV.prepend_path "PATH", formula_opt_bin("yarn")
    ENV.prepend_path "PATH", formula_opt_bin("mariadb")
    # Git exec path (needed by bench init)
    git_exec = Utils.safe_popen_read("git", "--exec-path").chomp
    ENV["GIT_EXEC_PATH"] = git_exec unless git_exec.empty?
    ENV["PYTHON"] = python_bin.to_s

    # bench's `get_mariadb_pkgconfig_path()` calls `brew --prefix mariadb-connector-c`
    # which is blocked inside Homebrew's Seatbelt build sandbox. Pre-set the vars
    # so pip install mysqlclient never needs to shell out to brew.
    mariadb_prefix = formula_opt_prefix("mariadb")
    mariadb_inc    = mariadb_prefix/"include/mysql"
    mariadb_lib    = mariadb_prefix/"lib"
    ENV["PKG_CONFIG_PATH"]     = "#{mariadb_lib}/pkgconfig:#{ENV["PKG_CONFIG_PATH"]}"
    ENV["MYSQLCLIENT_CFLAGS"]  = "-I#{mariadb_inc}"
    ENV["MYSQLCLIENT_LDFLAGS"] = "-L#{mariadb_lib} -lmariadb"
    ENV["MYSQL_CONFIG"]        = (mariadb_prefix/"bin/mariadb_config").to_s

    # ── Bootstrap venv for bench CLI ───────────────────────────────────────
    # A temporary venv to run `bench init`; the real venv is created by bench.
    bootstrap_venv = buildpath/"bootstrap-venv"
    system python_bin, "-m", "venv", bootstrap_venv
    pip = bootstrap_venv/"bin/pip"
    system pip, "install", "--quiet", "--upgrade", "pip", "setuptools", "wheel"
    system pip, "install", "--quiet", "frappe-bench==5.31.0"

    bench_cli = bootstrap_venv/"bin/bench"
    ENV.prepend_path "PATH", (bootstrap_venv/"bin").to_s

    # yarn: suppress engine mismatch noise
    system yarn_bin, "config", "set", "ignore-engines", "true"

    # ── brew stub: intercept bench's `brew --prefix mariadb-connector-c` ──
    brew_stub_dir = buildpath/"brew-stub"
    brew_stub_dir.mkpath
    brew_stub = brew_stub_dir/"brew"
    brew_stub.write <<~SH
      #!/bin/bash
      if [ "$1" = "--prefix" ] && [ "$2" = "mariadb-connector-c" ]; then
        echo "#{mariadb_prefix}"
        exit 0
      fi
      exec "#{HOMEBREW_PREFIX}/bin/brew" "$@" 2>/dev/null || true
    SH
    chmod 0755, brew_stub
    ENV.prepend_path "PATH", brew_stub_dir.to_s

    # ── bench init ─────────────────────────────────────────────────────────
    # Creates libexec/frappe-bench with Python venv, apps/frappe, and sites/.
    libexec.mkpath
    system bench_cli, "init",
           "--frappe-branch", "version-16",
           "--python", python_bin.to_s,
           "--no-backups",
           "--skip-redis-config-generation",
           "--verbose",
           bench_src.to_s

    # ── Ensure correct bench version in the snap venv too ─────────────────
    venv_pip = bench_src/"env/bin/pip"
    system venv_pip, "install", "frappe-bench==5.31.0"
    system venv_pip, "install", "--upgrade", "click~=8.3.1"

    # ── Fetch ERPNext v16 ──────────────────────────────────────────────────
    cd bench_src do
      system bench_src/"env/bin/bench", "get-app", "erpnext", "--branch", "version-16"
    end

    # ── Fix venv interpreter symlinks ──────────────────────────────────────
    venv_bin = bench_src/"env/bin"
    (venv_bin/"python3.14").unlink if (venv_bin/"python3.14").exist?
    (venv_bin/"python3").unlink    if (venv_bin/"python3").exist?
    (venv_bin/"python").unlink     if (venv_bin/"python").exist?
    venv_bin.install_symlink python_bin => "python3.14"
    venv_bin.install_symlink venv_bin/"python3.14" => "python3"
    venv_bin.install_symlink venv_bin/"python3.14" => "python"

    # Fix build-time shebangs
    Dir["#{venv_bin}/*"].each do |f|
      next unless File.file?(f)

      content = File.read(f)
      next unless content.start_with?("#!")

      fixed = content.sub(/\A#!.*python.*/, "#!/usr/bin/env python3")
      File.write(f, fixed) if fixed != content
    end

    # ── pyvenv.cfg — point at Homebrew python ──────────────────────────────
    pyvenv_cfg = bench_src/"env/pyvenv.cfg"
    if pyvenv_cfg.exist?
      content = pyvenv_cfg.read
      content.gsub!(/^home = .*$/, "home = #{python_bin.dirname}")
      content.gsub!(/^executable = .*$/, "executable = #{python_bin}")
      content.gsub!(/^base-executable = .*$/, "base-executable = #{python_bin}")
      File.write(pyvenv_cfg, content)
    end

    # ── Relativise asset symlinks ──────────────────────────────────────────
    assets_dir = bench_src/"sites/assets"
    if assets_dir.exist?
      Pathname.glob(assets_dir/"*").each do |link|
        next unless link.symlink?

        target = link.readlink.to_s
        next if !target.start_with?(buildpath.to_s) && !target.start_with?(libexec.to_s)

        rel = target.sub("#{bench_src}/", "")
        link.unlink
        link.make_symlink("../../#{rel}")
      end
    end

    # ── Seed common_site_config.json (build-time default) ─────────────────
    sites_cfg = bench_src/"sites/common_site_config.json"
    cfg_src = buildpath/"brew/etc/vybench/common_site_config.json"
    cp cfg_src, sites_cfg if cfg_src.exist?

    # ── Bypass Homebrew Linkage Checker ────────────────────────────────────
    # Precompiled wheels (like nh3) lack -headerpad_max_install_names and fail
    # Homebrew's linkage fix phase. Hide the venv in a tarball until post_install.
    cd bench_src do
      system "tar", "-czf", "env.tar.gz", "env"
      rm_r "env"
    end

    # Prebuilt Node add-ons in the apps' node_modules (lightningcss and the
    # like) are linked the same way, and the fix phase fails on them too
    # ("Updated load commands do not fit in the header"). They only matter
    # when assets are rebuilt. Pack them as well; post_install puts them back.
    cd bench_src do
      pwd = Pathname.pwd
      addons = Dir.glob("apps/**/node_modules/**/*.{node,dylib}").select do |f|
        File.file?(f) && !File.symlink?(f)
      end.map do |f|
        Pathname.new(File.realpath(f)).relative_path_from(pwd).to_s
      end.uniq
      unless addons.empty?
        File.write("native-addons.list", "#{addons.join("\n")}\n")
        system "tar", "-czf", "native-addons.tar.gz", "-T", "native-addons.list"
        rm addons, force: true
        rm "native-addons.list"
      end
    end

    # ── Install libexec scripts ────────────────────────────────────────────
    libexec.install buildpath/"brew/libexec/vybench-common.sh"
    libexec.install buildpath/"brew/libexec/vybench-supervisor"
    libexec.install buildpath/"brew/libexec/vybench-mariadb-wrapper"
    chmod 0755, libexec/"vybench-supervisor"
    chmod 0755, libexec/"vybench-common.sh"
    chmod 0755, libexec/"vybench-mariadb-wrapper"

    # ── Build the TUI and fpm ──────────────────────────────────────────────
    # vybench-tui is the interactive TUI and the multi-bench CLI behind
    # `vybench bench list|current|switch|new|attach|drop`. Release tarballs
    # older than it have no tui/ directory and build without it.
    if (buildpath/"tui/go.mod").exist?
      cd "tui" do
        system "go", "build", *std_go_args(ldflags: "-s -w -X main.version=#{version}", output: bin/"vybench-tui")
      end
    end

    # fpm goes in libexec, not bin: the unrelated Ruby packaging tool is also
    # called fpm. vybench-common.sh exports its path as VYBENCH_FPM.
    resource("fpm").stage do
      fpm_ldflags = "-s -w -X fpm/cmd.version=v#{resource("fpm").version}"
      system "go", "build", *std_go_args(ldflags: fpm_ldflags, output: libexec/"bin/fpm"), "./cmd/fpm"
    end

    # ── Install bin wrapper ────────────────────────────────────────────────
    bin.install buildpath/"brew/bin/vybench"
    chmod 0755, bin/"vybench"

    # ── Install etc config ─────────────────────────────────────────────────
    (etc/"vybench").mkpath
    cfg_dst = etc/"vybench/common_site_config.json"
    cfg_dst.write((buildpath/"brew/etc/vybench/common_site_config.json").read) unless cfg_dst.exist?
  end

  def post_install
    vybench_var  = var/"vybench"
    vybench_run  = var/"run/vybench"
    vybench_log  = var/"log/vybench"
    bench_root   = vybench_var/"bench"
    mariadb_data = vybench_var/"mariadb"
    redis_data   = vybench_var/"redis"

    # ── Data directories ───────────────────────────────────────────────────
    [vybench_var, vybench_run, vybench_log, mariadb_data, redis_data,
     bench_root/"logs", bench_root/"config/pids", bench_root/"sites"].each(&:mkpath)

    # ── Rewrite socket path in etc config ─────────────────────────────────
    cfg_path = etc/"vybench/common_site_config.json"
    if cfg_path.exist?
      content = cfg_path.read
      content.gsub!("VYBENCH_RUN_PLACEHOLDER", vybench_run.to_s)
      File.write(cfg_path, content)
    end

    # ── Seed bench sites config from etc ──────────────────────────────────
    bench_cfg = bench_root/"sites/common_site_config.json"
    cp cfg_path, bench_cfg if cfg_path.exist? && !bench_cfg.exist?

    # ── Extract hidden venv ───────────────────────────────────────────────
    bench_src = libexec/"frappe-bench"
    if (bench_src/"env.tar.gz").exist?
      cd bench_src do
        system "tar", "-xzf", "env.tar.gz"
        rm "env.tar.gz"
      end
    end

    # Put back the Node add-ons hidden from the linkage fix phase
    if (bench_src/"native-addons.tar.gz").exist?
      cd bench_src do
        system "tar", "-xzf", "native-addons.tar.gz"
        rm "native-addons.tar.gz"
      end
    end

    # ── apps/ and env/ symlinks into libexec ──────────────────────────────
    %w[apps env].each do |tree|
      link = bench_root/tree
      link.make_symlink(bench_src/tree) if !link.exist? && !link.symlink?
    end

    # ── sites/assets symlink ───────────────────────────────────────────────
    assets_link = bench_root/"sites/assets"
    assets_link.make_symlink(bench_src/"sites/assets") if !assets_link.exist? && !assets_link.symlink?

    # ── Seed apps.txt ──────────────────────────────────────────────────────
    ["apps.txt", "apps.json"].each do |f|
      dst = bench_root/"sites/#{f}"
      src = bench_src/"sites/#{f}"
      File.write(dst, src.read) if src.exist? && !dst.exist?
    end
  end

  # ── Service block (Frappe application tier only) ────────────────────────
  # MariaDB and Redis run as their own separate brew services.
  service do
    run [opt_libexec/"vybench-supervisor"]
    keep_alive true
    log_path var/"log/vybench/supervisor.log"
    error_log_path var/"log/vybench/supervisor.err.log"
    environment_variables(
      HOMEBREW_PREFIX: HOMEBREW_PREFIX,
      VYBENCH_LIBEXEC: opt_libexec.to_s,
      VYBENCH_ETC:     (etc/"vybench").to_s,
      VYBENCH_VAR:     (var/"vybench").to_s,
      VYBENCH_LOG:     (var/"log/vybench").to_s,
      VYBENCH_RUN:     (var/"run/vybench").to_s,
      PATH:            "#{formula_opt_bin("python@3.14")}:#{formula_opt_bin("node")}:" \
                       "#{formula_opt_bin("mariadb")}:#{formula_opt_bin("redis")}:" \
                       "#{HOMEBREW_PREFIX}/bin:/usr/bin:/bin",
    )
  end

  def caveats
    vybench_var = var/"vybench"
    vybench_run = var/"run/vybench"
    <<~EOS
      Vybench has been installed.

      ── QUICK START ────────────────────────────────────────────────────────

      1. Start vybench. It runs its own MariaDB (port 13306) and Redis
         (port 16379), so Homebrew's mariadb and redis services are not
         needed and can keep running for other projects:
           vybench start

      2. Or open the interactive TUI:
           vybench tui

      3. Create your first site:
           vybench bench new-site mysite.localhost --admin-password admin
           echo "127.0.0.1 mysite.localhost" | sudo tee -a /etc/hosts
           # → Open http://mysite.localhost:8000

      ── MODES ──────────────────────────────────────────────────────────────

        vybench set mode=developer       writable bench, bench serve
        vybench set mode=production      gunicorn + managed workers (default)

      ── PATHS ──────────────────────────────────────────────────────────────

        Sites & config   #{vybench_var}/bench/sites/
        Database files   #{vybench_var}/mariadb/
        Logs             #{var}/log/vybench/
        MariaDB socket   #{vybench_run}/mysql.sock

      ── PDF GENERATION ─────────────────────────────────────────────────────

        Frappe requires wkhtmltopdf with patched Qt for PDF output:
          brew install --cask wkhtmltopdf

      ── DOCUMENTATION ──────────────────────────────────────────────────────

        https://github.com/vyogotech/vybench
    EOS
  end

  test do
    # Verify the CLI wrapper is executable and shows help
    assert_match "Frappe Bench", shell_output("#{bin}/vybench help")

    # Verify the supervisor script is syntactically valid bash
    system "bash", "-n", libexec/"vybench-supervisor"
    system "bash", "-n", libexec/"vybench-common.sh"

    # Verify python3 interpreter exists
    assert_predicate libexec/"frappe-bench/env/bin/python3", :executable?
    # The TUI and fpm are only built from sources that include tui/
    if (bin/"vybench-tui").exist?
      assert_match "vybench-tui #{version}", shell_output("#{bin}/vybench-tui --version")
      assert_predicate libexec/"bin/fpm", :executable?
    end
  end
end
