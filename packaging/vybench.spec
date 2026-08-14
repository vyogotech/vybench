# Vybench -- Frappe Framework / ERPNext as a native RPM.
#
# Driven by scripts/package-rpm.sh, which stages the complete filesystem into a
# tarball and passes the payload's own metadata in via --define. The spec never
# builds anything: the bench is built by scripts/build-bench-payload.sh inside a
# container of the target distribution, at /opt/frappe-bench, which is the only
# way the venv's absolute paths and the asset symlinks come out correct.
#
# Required defines:
#   vybench_version  vybench_arch  python_xy  distro_tag  frappe_branch

%global bench_dir /opt/frappe-bench
# Bundled CPython 3.14 -- see the Requires block for why it is bundled at all.
%global python_home /usr/lib/vybench/python

# _unitdir comes from systemd-rpm-macros, which is NOT installed in a minimal
# RHEL 9 / Rocky 9 build root -- rpmbuild then leaves the macro unexpanded and
# fails with "File must begin with /". Defining it as a fallback keeps the spec
# buildable without pulling in a BuildRequires purely for one path.
%{!?_unitdir: %global _unitdir /usr/lib/systemd/system}

# The payload is a prebuilt tree, not something rpmbuild compiled. Every default
# post-install brp script is wrong for it:
#   - brp-python-bytecompile would byte-compile a venv with the SYSTEM python,
#     writing .pyc files that the venv's interpreter then has to invalidate --
#     and on a version mismatch, refuses to load.
#   - brp-strip would strip node and the compiled extension modules.
#   - check-rpaths objects to node's legitimate RPATHs.
%global __os_install_post %{nil}
%global __brp_check_rpaths %{nil}
%global debug_package %{nil}
%global _build_id_links none

# Autodetected dependencies on a tree this size are noise: rpm would scan every
# vendored .so and every python module in site-packages and emit hundreds of
# unresolvable Requires, plus Provides that shadow the distribution's own python
# packages. The real dependencies are the short list below.
AutoReqProv: no

Name:           vybench
Version:        %{vybench_version}
Release:        1.%{distro_tag}%{?dist}
Summary:        Frappe Framework and ERPNext, packaged for Fedora and RHEL
License:        GPL-3.0-or-later
URL:            https://github.com/vyogotech/vybench
Source0:        vybench-root-%{version}.tar.gz
BuildArch:      %{vybench_arch}

# Uses the distribution's datastores and web server rather than bundling them --
# that is the point of a native package, as opposed to the snap.
Requires:       mariadb-server >= 10.6
Requires:       mariadb
Requires:       redis >= 6.0
Requires:       nginx
Requires:       mariadb-connector-c
# No python dependency: frappe v16 requires exactly 3.14, which no Fedora or RHEL
# release in this matrix ships, so the interpreter is bundled at
# /usr/lib/vybench/python and the system python is not used at all.
#
# The bundled interpreter does link against this release's OpenSSL, libffi and
# sqlite. Those Requires are resolved at build time by asking rpm which packages
# own the libraries it actually loads, and passed in via --define as one
# space-separated list (rpm treats each name on the line as a separate Requires).
%{?runtime_requires:Requires: %{runtime_requires}}
# envsubst, for rendering the nginx vhost in vybench-setup.
Requires:       gettext
# runuser, used by vybench-setup instead of sudo (minimal images have no sudo).
Requires:       util-linux
Requires:       git
Requires:       curl
Requires:       ca-certificates

# The package was called frappista before the project was renamed. Without this
# a user who installed the old one ends up with two packages owning the same
# files in /opt/frappe-bench, and rpm refuses the transaction.
Obsoletes:      frappista < %{version}-%{release}
Obsoletes:      frappista-bench < %{version}-%{release}
Obsoletes:      frappium < %{version}-%{release}
Obsoletes:      frappium-bench < %{version}-%{release}

Requires(pre):  shadow-utils
Requires(post): systemd
Requires(preun): systemd
Requires(postun): systemd

# PDF generation only. Not in every RHEL repository, and a hard dependency would
# make the package uninstallable on a machine that does not need PDFs.
Recommends:     wkhtmltopdf
Recommends:     fontconfig

%description
A complete single-node Frappe/ERPNext deployment: the bench (apps, virtualenv
and prebuilt assets) together with systemd units for the web server, the three
background worker queues, the scheduler and the realtime server.

Python 3.14 is bundled at %{python_home}, because frappe v16 requires exactly
3.14 and no Fedora or RHEL release in this matrix ships it.

Built for %{distro_tag}, frappe %{frappe_branch}.

Run 'vybench-setup' after installing to create the database account, the first
site and the nginx vhost. Nothing starts before you do.

%prep
# Nothing to unpack here -- the staged tree goes straight into the buildroot.
%setup -q -c -T

%build
# Deliberately empty. See the header: building happens in a container, at the
# final install path, before rpmbuild is ever invoked.

%install
rm -rf %{buildroot}
mkdir -p %{buildroot}
tar -xzf %{SOURCE0} -C %{buildroot}

# Guard against a stager change that silently stops shipping the bench: an RPM
# that installs an empty /opt/frappe-bench is exactly the failure mode the old
# .deb had, and it reports success.
test -x %{buildroot}%{bench_dir}/env/bin/bench
test -x %{buildroot}%{bench_dir}/env/bin/gunicorn
test -x %{buildroot}%{bench_dir}/node/bin/node
test -f %{buildroot}%{bench_dir}/sites/apps.txt
test -x %{buildroot}%{python_home}/bin/python3.14

%pre
# bench refuses to run as root, and frappe's change_uid() calls getpwnam() on the
# configured frappe_user -- a KeyError there kills the process outright. The
# account has to exist before any file lands. Home is the bench itself, so
# nothing scatters into /home.
getent group frappe >/dev/null || groupadd -r frappe
getent passwd frappe >/dev/null || \
    useradd -r -g frappe -d %{bench_dir} -s /sbin/nologin \
            -c "Frappe application account" frappe
exit 0

%post
# bench's is_bench_directory() requires apps, sites, config, logs AND
# config/pids. Miss one -- config/pids is the one that goes missing -- and bench
# stops loading frappe's subcommands, so the worker units die with
# "Error: No such command 'worker'".
install -d -o frappe -g frappe -m 2770 \
    %{bench_dir}/apps %{bench_dir}/sites %{bench_dir}/config \
    %{bench_dir}/config/pids %{bench_dir}/logs

# The payload carries numeric 0:0 ownership because the frappe uid is not known
# at build time. Group-shared, never world-writable -- the container image's
# model (chown 1001:0 + chmod ug+rwX) with frappe:frappe in place of 1001:0.
chown -R frappe:frappe %{bench_dir}
chmod -R ug+rwX,o-rwx %{bench_dir}
# Nothing here is world-readable: sites/*/private holds uploaded documents and
# site_config.json holds the database password. nginx gets access by being put in
# the frappe group (vybench-setup does that), not by opening the tree up.

# Everyone reaches for `bench`. Only claim the name if it is free: a
# pip-installed frappe-bench in /usr/local/bin is a common setup and silently
# replacing it would be worse than not having the shortcut.
if [ ! -e /usr/local/bin/bench ]; then
    ln -sf %{bench_dir}/env/bin/bench /usr/local/bin/bench
fi

systemctl daemon-reload >/dev/null 2>&1 || :

if [ $1 -eq 1 ]; then
cat <<'MSG'

Vybench is installed but not yet configured.

  sudo vybench-setup --site dev.localhost --admin-password admin

That creates the database account, the site, the nginx vhost, and starts the
services. Nothing is running before you do.
MSG
fi
exit 0

%preun
if [ $1 -eq 0 ]; then
    # Removal, not an upgrade. Stop the application tier only; mariadb, redis
    # and nginx are shared with the rest of the system.
    systemctl stop 'frappe-worker@*.service' >/dev/null 2>&1 || :
    systemctl stop frappe-web.service frappe-scheduler.service \
                   frappe-socketio.service vybench.target >/dev/null 2>&1 || :
    systemctl disable 'frappe-worker@*.service' >/dev/null 2>&1 || :
    systemctl disable frappe-web.service frappe-scheduler.service \
                      frappe-socketio.service vybench.target >/dev/null 2>&1 || :
fi
exit 0

%postun
if [ $1 -eq 0 ]; then
    if [ -L /usr/local/bin/bench ] && \
       [ "$(readlink /usr/local/bin/bench)" = "%{bench_dir}/env/bin/bench" ]; then
        rm -f /usr/local/bin/bench
    fi
    # vybench-setup wrote this one; rpm does not know about it.
    rm -f /etc/nginx/conf.d/frappe.conf
    systemctl reload nginx >/dev/null 2>&1 || :

    # Sites, uploaded files and databases are left alone, deliberately. They are
    # the user's data, not package files, and rpm cannot tell "uninstalling"
    # from "about to reinstall".
    if [ -d %{bench_dir}/sites ]; then
        echo "Site data remains in %{bench_dir}/sites and the databases remain in MariaDB."
    fi
fi
systemctl daemon-reload >/dev/null 2>&1 || :
exit 0

%files
%defattr(-,root,root,-)
%{_bindir}/vybench-setup
%dir /etc/vybench
%dir /etc/vybench/nginx
%config(noreplace) /etc/vybench/vybench.env
/etc/vybench/nginx/frappe.conf.template
%config(noreplace) /etc/my.cnf.d/10-frappe.cnf
%{_unitdir}/vybench.target
%{_unitdir}/frappe-web.service
%{_unitdir}/frappe-worker@.service
%{_unitdir}/frappe-scheduler.service
%{_unitdir}/frappe-socketio.service
%{bench_dir}
%dir /usr/lib/vybench
%{python_home}
%doc %{_datadir}/doc/vybench

%changelog
* Fri Aug 14 2026 Vyogo Labs <dev@vyogolabs.tech> - 16.0.0-1
- First native RPM. Ships a real bench payload built at the final install path.
- Per-queue worker template unit, scheduler and socketio units added.
- vybench-setup provisions a dedicated MariaDB admin account, so site
  creation is non-interactive.
