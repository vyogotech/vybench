"""Runtime compatibility shims for vybench under strict Snap confinement.

Under strict Snap confinement, syscalls such as ioprio_set(2) (and sometimes
setpriority(2)) are blocked by snapd's seccomp/AppArmor profile for unprivileged
daemons. Frappe's frappe.commands.popen() uses preexec_fn=set_low_prio, which
imports psutil and calls psutil.Process().nice(19) and ionice(...); any
uncaught exception in preexec_fn aborts subprocess.Popen before exec.

Installing this shim via sitecustomize.py on PYTHONPATH keeps apps/frappe
completely unmodified (so `git -C apps/frappe status` stays clean for `bench
update` and audits) and uses a lazy import hook so psutil is not imported at
Python startup when unused.
"""

import sys


def _patch_psutil(mod):
    if getattr(mod, "_vybench_low_prio_patched", False):
        return
    mod._vybench_low_prio_patched = True

    process_cls = getattr(mod, "Process", None)
    if process_cls is None:
        return

    access_denied = getattr(mod, "AccessDenied", None)
    catch_types = (OSError,) if access_denied is None else (access_denied, OSError)

    orig_nice = getattr(process_cls, "nice", None)
    if callable(orig_nice):
        def _safe_nice(self, value=None):
            if value is None:
                return orig_nice(self)
            try:
                return orig_nice(self, value)
            except catch_types:
                return None

        process_cls.nice = _safe_nice

    orig_ionice = getattr(process_cls, "ionice", None)
    if callable(orig_ionice):
        def _safe_ionice(self, ioclass=None, value=None):
            if ioclass is None and value is None:
                return orig_ionice(self)
            try:
                if value is None:
                    return orig_ionice(self, ioclass)
                return orig_ionice(self, ioclass, value)
            except catch_types:
                return None

        process_cls.ionice = _safe_ionice


class _PatchedLoader:
    def __init__(self, loader):
        self._loader = loader

    def create_module(self, spec):
        if hasattr(self._loader, "create_module"):
            return self._loader.create_module(spec)
        return None

    def exec_module(self, module):
        self._loader.exec_module(module)
        _patch_psutil(module)

    def __getattr__(self, name):
        return getattr(self._loader, name)


class _VybenchPsutilFinder:
    def __init__(self):
        self._active = False

    def find_spec(self, fullname, path, target=None):
        if fullname != "psutil" or self._active:
            return None
        self._active = True
        try:
            for finder in list(sys.meta_path):
                if type(finder).__name__ == "_VybenchPsutilFinder":
                    continue
                find_spec = getattr(finder, "find_spec", None)
                if find_spec is None:
                    continue
                spec = find_spec(fullname, path, target)
                if spec is not None:
                    if spec.loader is not None and hasattr(spec.loader, "exec_module"):
                        spec.loader = _PatchedLoader(spec.loader)
                    return spec
            return None
        finally:
            self._active = False


if "psutil" in sys.modules:
    try:
        _patch_psutil(sys.modules["psutil"])
    except Exception:
        pass

if not any(type(f).__name__ == "_VybenchPsutilFinder" for f in sys.meta_path):
    sys.meta_path.insert(0, _VybenchPsutilFinder())
