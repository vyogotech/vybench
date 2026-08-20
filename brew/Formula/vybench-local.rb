# typed: false
# frozen_string_literal: true

# LOCAL TEST FORMULA — uses a file:// tarball of the working tree.
# Not for distribution. The canonical formula is brew/Formula/vybench.rb.
#
# Install:
#   brew install --build-from-source brew/Formula/vybench-local.rb
#
# Uninstall:
#   brew uninstall vybench-local
class VybenchLocal < Formula
  desc "Frappe Bench v16 & ERPNext v16 — local test build"
  homepage "https://github.com/vyogotech/vybench"
  # Points at the local tarball built by:
  #   rsync + tar (see Makefile `brew-local-test` target)
  url "file:///tmp/vybench-test.tar.gz"
  version "16.0.0-local"
  sha256 "97b441320749c0dcee55e10e36e81443f2b8391d13063e3b1ff64eb597385e1b"
  license "GPL-3.0-only"

  # ── Runtime dependencies ──────────────────────────────────────────────────
  depends_on :macos
  depends_on "mariadb"
  depends_on "node"
  depends_on "python@3.14"
  depends_on "redis"
  depends_on "yarn"

  # ── Build-time resource: frappe-bench CLI ─────────────────────────────────
  resource "frappe-bench" do
    url "https://files.pythonhosted.org/packages/59/bd/21a8d447f9c6b6df651753fee40899def1004987a348131112b1d7470390/frappe_bench-5.31.0.tar.gz"
    sha256 "bcc829befe2fb6c5145cd6f1ebb6d5a8b34bcf58de99cb8a6afc0e88430776c5"
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
    ENV["PYTHON"] = python_bin.to_s

    # Git exec path (needed by bench init)
    git_exec = Utils.safe_popen_read("git", "--exec-path").chomp
    ENV["GIT_EXEC_PATH"] = git_exec unless git_exec.empty?

    # bench's `get_mariadb_pkgconfig_path()` calls `brew --prefix mariadb-connector-c`
    # which is blocked inside Homebrew's Seatbelt build sandbox. Pre-set the vars
    # so pip install mysqlclient never needs to shell out to brew.
    mariadb_prefix = formula_opt_prefix("mariadb")
    mariadb_inc    = mariadb_prefix/"include/mysql"
    mariadb_lib    = mariadb_prefix/"lib"
    ENV["PKG_CONFIG_PATH"]     = "#{mariadb_lib}/pkgconfig:#{ENV["PKG_CONFIG_PATH"]}"
    ENV["MYSQLCLIENT_CFLAGS"]  = "-I#{mariadb_inc}"
    ENV["MYSQLCLIENT_LDFLAGS"] = "-L#{mariadb_lib} -lmariadb"
    # Also satisfy mysql_config lookup used by older mysqlclient builds
    ENV["MYSQL_CONFIG"]        = (mariadb_prefix/"bin/mariadb_config").to_s

    # ── Bootstrap venv for running bench init ─────────────────────────────
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
    # bench/utils/system.py calls subprocess(["brew", "--prefix", "mariadb-connector-c"])
    # to find MariaDB headers for building mysqlclient. The real `brew` binary is
    # blocked inside Homebrew's macOS Seatbelt sandbox. We drop a stub script earlier
    # on PATH that returns the correct mariadb prefix for that exact call, and passes
    # everything else through to the real brew (which will also fail in sandbox, but
    # bench only calls it for this one purpose during pip install mysqlclient).
    brew_stub_dir = buildpath/"brew-stub"
    brew_stub_dir.mkpath
    brew_stub = brew_stub_dir/"brew"
    brew_stub.write <<~SH
      #!/bin/bash
      # Stub brew intercepting mariadb-connector-c prefix lookup during bench init.
      if [ "$1" = "--prefix" ] && [ "$2" = "mariadb-connector-c" ]; then
        echo "#{mariadb_prefix}"
        exit 0
      fi
      # For any other call, try the real brew (may fail in sandbox — that's OK)
      exec "#{HOMEBREW_PREFIX}/bin/brew" "$@" 2>/dev/null || true
    SH
    chmod 0755, brew_stub
    ENV.prepend_path "PATH", brew_stub_dir.to_s

    # ── bench init ─────────────────────────────────────────────────────────
    libexec.mkpath
    system bench_cli, "init",
           "--frappe-branch", "version-16",
           "--python", python_bin.to_s,
           "--no-backups",
           "--skip-redis-config-generation",
           "--verbose",
           bench_src.to_s

    # ── Ensure bench CLI is in the snap venv too ──────────────────────────
    venv_pip = bench_src/"env/bin/pip"
    system venv_pip, "--quiet", "install", "frappe-bench==5.31.0"
    system venv_pip, "--quiet", "install", "--upgrade", "click~=8.3.1"

    # ── Fetch ERPNext v16 ─────────────────────────────────────────────────
    cd bench_src do
      system bench_src/"env/bin/bench", "get-app", "erpnext", "--branch", "version-16"
    end

    # ── Fix venv interpreter symlinks ──────────────────────────────────────
    venv_bin = bench_src/"env/bin"
    %w[python3.14 python3 python].each { |f| (venv_bin/f).unlink if (venv_bin/f).symlink? }
    venv_bin.install_symlink python_bin => "python3.14"
    venv_bin.install_symlink venv_bin/"python3.14" => "python3"
    venv_bin.install_symlink venv_bin/"python3.14" => "python"

    # Fix build-time shebangs
    Dir["#{venv_bin}/*"].each do |f|
      next unless File.file?(f)

      lines = File.readlines(f)
      next unless lines.first&.start_with?("#!")

      lines[0] = "#!/usr/bin/env python3\n" if lines[0].include?("python")
      File.write(f, lines.join)
    end

    # ── pyvenv.cfg ────────────────────────────────────────────────────────
    pyvenv_cfg = bench_src/"env/pyvenv.cfg"
    if pyvenv_cfg.exist?
      content = pyvenv_cfg.read
      content.gsub!(/^home = .*$/, "home = #{python_bin.dirname}")
      content.gsub!(/^executable = .*$/, "executable = #{python_bin}")
      content.gsub!(/^base-executable = .*$/, "base-executable = #{python_bin}")
      File.write(pyvenv_cfg, content)
    end

    # ── Relativise sites/assets symlinks ─────────────────────────────────
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

    # ── Bypass Homebrew Linkage Checker ────────────────────────────────────
    # Precompiled wheels (like nh3) lack -headerpad_max_install_names and fail
    # Homebrew's linkage fix phase. Hide the venv in a tarball until post_install.
    cd bench_src do
      system "tar", "-czf", "env.tar.gz", "env"
      rm_rf "env"
    end

    # ── Install libexec scripts ───────────────────────────────────────────
    # The buildpath is the extracted tarball root (vybench-16.0.0/)
    brew_dir = buildpath/"brew"
    libexec.install brew_dir/"libexec/vybench-common.sh"
    libexec.install brew_dir/"libexec/vybench-supervisor"
    libexec.install brew_dir/"libexec/vybench-mariadb-wrapper"
    chmod 0755, libexec/"vybench-supervisor"
    chmod 0755, libexec/"vybench-common.sh"
    chmod 0755, libexec/"vybench-mariadb-wrapper"

    # ── Install bin wrapper ───────────────────────────────────────────────
    bin.install brew_dir/"bin/vybench"
    chmod 0755, bin/"vybench"

    # ── Install etc config ────────────────────────────────────────────────
    (etc/"vybench-local").mkpath
    cfg_dst = etc/"vybench-local/common_site_config.json"
    cfg_dst.write((brew_dir/"etc/vybench/common_site_config.json").read) unless cfg_dst.exist?
  end

  def post_install
    vybench_var = var/"vybench-local"
    vybench_run = var/"run/vybench-local"
    vybench_log = var/"log/vybench-local"
    bench_root  = vybench_var/"bench"

    [vybench_var, vybench_run, vybench_log,
     vybench_var/"mariadb", vybench_var/"redis",
     bench_root/"logs", bench_root/"config/pids", bench_root/"sites"].each(&:mkpath)

    # Rewrite socket placeholder in etc config
    cfg_path = etc/"vybench-local/common_site_config.json"
    if cfg_path.exist?
      content = cfg_path.read
      content.gsub!("VYBENCH_RUN_PLACEHOLDER", vybench_run.to_s)
      File.write(cfg_path, content)
    end

    # Seed bench sites config
    bench_cfg = bench_root/"sites/common_site_config.json"
    cp cfg_path, bench_cfg if cfg_path.exist? && !bench_cfg.exist?

    # Extract hidden venv
    bench_src = libexec/"frappe-bench"
    if (bench_src/"env.tar.gz").exist?
      cd bench_src do
        system "tar", "-xzf", "env.tar.gz"
        rm_f "env.tar.gz"
      end
    end

    # apps/ and env/ symlinks
    %w[apps env].each do |tree|
      link = bench_root/tree
      link.make_symlink(bench_src/tree) if !link.exist? && !link.symlink?
    end

    # sites/assets symlink
    assets_link = bench_root/"sites/assets"
    assets_link.make_symlink(bench_src/"sites/assets") if !assets_link.exist? && !assets_link.symlink?

    # Seed apps.txt / apps.json
    %w[apps.txt apps.json].each do |f|
      dst = bench_root/"sites/#{f}"
      src = bench_src/"sites/#{f}"
      File.write(dst, src.read) if src.exist? && !dst.exist?
    end
  end

  service do
    run [opt_libexec/"vybench-supervisor"]
    keep_alive true
    log_path var/"log/vybench-local/supervisor.log"
    error_log_path var/"log/vybench-local/supervisor.err.log"
    environment_variables(
      HOMEBREW_PREFIX: HOMEBREW_PREFIX,
      VYBENCH_LIBEXEC: opt_libexec.to_s,
      VYBENCH_ETC:     (etc/"vybench-local").to_s,
      VYBENCH_VAR:     (var/"vybench-local").to_s,
      VYBENCH_LOG:     (var/"log/vybench-local").to_s,
      VYBENCH_RUN:     (var/"run/vybench-local").to_s,
      BENCH_ROOT:      (var/"vybench-local/bench").to_s,
      PATH:            "#{formula_opt_bin("python@3.14")}:#{formula_opt_bin("node")}:" \
                       "#{formula_opt_bin("mariadb")}:#{formula_opt_bin("redis")}:" \
                       "#{HOMEBREW_PREFIX}/bin:/usr/bin:/bin",
    )
  end

  def caveats
    vybench_var = var/"vybench-local"
    vybench_run = var/"run/vybench-local"
    <<~EOS
      LOCAL TEST BUILD of vybench installed.

        brew services start mariadb
        brew services start redis
        brew services start vybench-local

        vybench bench new-site test.localhost --admin-password admin

      Paths:
        Sites:   #{vybench_var}/bench/sites/
        DB:      #{vybench_var}/mariadb/
        Logs:    #{var}/log/vybench-local/
        Socket:  #{vybench_run}/mysql.sock
    EOS
  end

  test do
    assert_match "Frappe Bench", shell_output("#{bin}/vybench help")
    system "bash", "-n", libexec/"vybench-supervisor"
    system "bash", "-n", libexec/"vybench-common.sh"
    assert_predicate libexec/"frappe-bench/env/bin/python3", :executable?
  end
end
