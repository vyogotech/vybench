package core

// vybench doctor: the checks that turn a bench which "just stopped working"
// into a named, repairable fault.
//
// Every check here exists because of a failure seen on a real install. The one
// that motivated the file is app registration: fpm installs an app by dropping
// three things into the bench -- an apps/<app> symlink, a line in
// sites/apps.txt, and an <app>.pth on the virtualenv's sys.path. Any two of the
// three without the third is an *incomplete registration*, and frappe's reaction
// to it is uninformative: setup_module_map() walks sites/apps.txt and calls
// importlib.import_module() on each name, so one unimportable app takes down
// `bench new-site`, every worker, and the whole frappe command group -- which is
// why the symptom reads "No such command 'new-site'" rather than anything about
// the app.
//
// The second family is access, which under strict snap confinement is not the
// same question as "are the mode bits right". root has CAP_DAC_OVERRIDE dropped,
// the services run as snap_daemon, and a path under a 0700 home (/root/.fpm/...)
// is unreachable no matter how permissive the leaf is. Those produce a bare
// "permission denied" from somewhere deep inside frappe.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Severity ranks a finding.
type Severity int

const (
	SevOK    Severity = iota // nothing wrong; recorded so the report shows what was checked
	SevWarn                  // works today, will bite later
	SevError                 // broken now
)

func (s Severity) String() string {
	switch s {
	case SevError:
		return "FAIL"
	case SevWarn:
		return "WARN"
	}
	return "OK"
}

// Finding is one thing doctor noticed.
type Finding struct {
	Check    string // the check that produced it, e.g. "apps"
	Title    string // one line, no trailing period
	Detail   string // what it means and what it breaks; may be several lines
	Severity Severity
	FixLabel string // what Fix would do; "" when doctor cannot repair it
	Fixed    bool
	FixErr   error

	fix func() error
}

// Fixable reports whether doctor can repair this itself.
func (f Finding) Fixable() bool { return f.fix != nil }

// Report is one doctor run.
type Report struct {
	BenchPath string
	Findings  []Finding
}

func (r *Report) add(f Finding) { r.Findings = append(r.Findings, f) }

// Problems returns the findings that are not OK.
func (r Report) Problems() []Finding {
	var out []Finding
	for _, f := range r.Findings {
		if f.Severity != SevOK {
			out = append(out, f)
		}
	}
	return out
}

// Worst is the highest severity in the report.
func (r Report) Worst() Severity {
	worst := SevOK
	for _, f := range r.Findings {
		if f.Severity > worst {
			worst = f.Severity
		}
	}
	return worst
}

// Healthy reports whether nothing needs attention.
func (r Report) Healthy() bool { return r.Worst() == SevOK }

// Apply runs every fix the report carries, in the order the checks found them
// -- layout before registration before permissions, because a repair often
// depends on the one before it. It returns how many succeeded and how many
// failed; the outcome of each is recorded on the finding.
func (r *Report) Apply() (fixed, failed int) {
	for i := range r.Findings {
		f := &r.Findings[i]
		if f.fix == nil || f.Severity == SevOK {
			continue
		}
		if err := f.fix(); err != nil {
			f.FixErr = err
			failed++
			continue
		}
		f.Fixed = true
		fixed++
	}
	return fixed, failed
}

// DoctorOptions tunes a run.
type DoctorOptions struct {
	// SkipNetwork leaves out the registry reachability check, which is the
	// only one that can take seconds.
	SkipNetwork bool
}

// RunDoctor inspects benchPath and returns everything it found. It changes
// nothing: call Report.Apply to repair.
func RunDoctor(ctx context.Context, benchPath string, opt DoctorOptions) Report {
	r := Report{BenchPath: benchPath}
	checkLayout(benchPath, &r)
	checkAppRegistration(benchPath, &r)
	checkAccess(benchPath, &r)
	checkWritableTrees(benchPath, &r)
	checkFPMStore(&r)
	checkBackupTools(&r)
	checkSites(benchPath, &r)
	checkDBRoot(benchPath, &r)
	if !opt.SkipNetwork {
		checkRegistry(ctx, &r)
	}
	return r
}

// ──────────────────────────────────────────────────────────────────────────────
// Layout
// ──────────────────────────────────────────────────────────────────────────────

// benchDirs are the directories frappe-bench's is_bench_directory() insists on
// and that a bench simply owns. Miss any one -- config/pids is the easy one to
// forget -- and bench decides it is not in a bench, never loads frappe's
// subcommands, and answers every site command with "No such command".
//
// apps/ and env/ are deliberately absent from this list. They are not made by
// mkdir: on a production install they are symlinks into the package's read-only
// bench, which is what holds frappe itself. Creating an empty apps/ directory
// here would not merely fail to help -- it would make the bench permanently
// broken, because the platform's own bootstrap only links a tree that is
// missing or already a symlink, and would now skip it forever.
var benchDirs = []string{"sites", "config", "config/pids", "logs"}

// benchTrees are linked at the packaged bench rather than created.
var benchTrees = []string{"apps", "env"}

func checkLayout(benchPath string, r *Report) {
	var missing []string
	for _, d := range benchDirs {
		if !isDir(filepath.Join(benchPath, d)) {
			missing = append(missing, d)
		}
	}
	if len(missing) > 0 {
		r.add(Finding{
			Check:    "layout",
			Severity: SevError,
			Title:    "the bench is missing " + strings.Join(missing, ", "),
			Detail: "bench only loads frappe's commands when apps/, sites/, config/, config/pids/ and logs/ all exist. " +
				"Without them every site command fails with \"No such command\".",
			FixLabel: "create " + strings.Join(missing, ", "),
			fix: func() error {
				for _, d := range missing {
					p := filepath.Join(benchPath, d)
					if err := daemonMkdirAll(p); err != nil {
						return err
					}
					chownDaemon(p)
				}
				return nil
			},
		})
	}

	// Linked through `current`, not through $SNAP: a link into the revision
	// directory dangles the moment the snap refreshes.
	packaged := StableBenchPath()
	var unlinked []string
	for _, t := range benchTrees {
		if !fileExists(filepath.Join(benchPath, t)) {
			unlinked = append(unlinked, t)
		}
	}
	appsTxtPath := filepath.Join(benchPath, "sites", "apps.txt")

	// A bench that has never been used. `vybench bench new` creates only the
	// directories; the snap's wrappers link apps/, env/, sites/assets and
	// apps.txt the first time a command or service runs on it (bootstrap_common).
	// So a bench made a minute ago has none of them, and reporting that as a
	// FAILURE -- exit 1 -- for the product's own output is a false alarm. Only
	// when ALL of it is absent: any of it present means something was set up and
	// then damaged, which is a real fault. Snap only, because that is the only
	// platform whose wrappers link lazily.
	if DetectPlatform() == PlatformSnap && len(missing) == 0 && len(unlinked) == len(benchTrees) &&
		!fileExists(appsTxtPath) && !fileExists(filepath.Join(benchPath, "sites", "assets")) {
		r.add(Finding{Check: "layout", Severity: SevOK,
			Title: "the bench is new and has not been used yet: its apps are linked the first time a command or service runs on it"})
		return
	}
	if len(unlinked) > 0 {
		f := Finding{
			Check:    "layout",
			Severity: SevError,
			Title:    "the bench has no " + strings.Join(unlinked, " and no "),
			Detail: "apps/ holds frappe itself and env/ its virtualenv. A bench without them has no code to run: every " +
				"bench command fails before it starts. On this platform they are symlinks into the bench shipped with the package.",
		}
		if packaged != "" && isDir(packaged) {
			f.FixLabel = "link them at " + packaged
			f.fix = func() error {
				for _, t := range unlinked {
					src := filepath.Join(packaged, t)
					if !fileExists(src) {
						return fmt.Errorf("%s does not exist, so %s cannot be linked", src, t)
					}
					if err := daemonSymlink(src, filepath.Join(benchPath, t)); err != nil {
						return err
					}
					chownDaemonLink(filepath.Join(benchPath, t))
				}
				return nil
			}
		}
		r.add(f)
	}

	// frappe discovers its apps through sites/apps.txt and raises OSError when
	// it is absent, which stops bench loading frappe's commands at all.
	appsTxt := filepath.Join(benchPath, "sites", "apps.txt")
	if isDir(filepath.Join(benchPath, "sites")) && !fileExists(appsTxt) {
		f := Finding{
			Check: "layout", Severity: SevError,
			Title:  "sites/apps.txt is missing",
			Detail: "frappe reads this file to find its apps and raises \"apps.txt Not Found\" without it, so bench never loads the frappe command group.",
		}
		seed := filepath.Join(packaged, "sites", "apps.txt")
		if packaged != "" && fileExists(seed) {
			f.FixLabel = "seed it from " + seed
			f.fix = func() error {
				data, err := os.ReadFile(seed)
				if err != nil {
					return err
				}
				return daemonWriteFile(appsTxt, data)
			}
		} else {
			f.FixLabel = "create it listing frappe"
			f.fix = func() error { return daemonWriteFile(appsTxt, []byte("frappe\n")) }
		}
		r.add(f)
	}

	if len(missing) == 0 && len(unlinked) == 0 && fileExists(appsTxt) {
		r.add(Finding{Check: "layout", Severity: SevOK, Title: "bench layout is complete"})
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// App registration
// ──────────────────────────────────────────────────────────────────────────────

func checkAppRegistration(benchPath string, r *Report) {
	listed := appsTxtNames(benchPath)
	onDisk := appsOnDisk(benchPath)
	sp := sitePackagesDir(benchPath)
	runtimeUID := runtimeUserID()

	// An app in apps/ that nobody listed is installed but not registered:
	// frappe never loads it, so its DocTypes and hooks are silently absent.
	var unlisted []string
	for _, app := range onDisk {
		if !contains(listed, app) {
			unlisted = append(unlisted, app)
		}
	}
	if len(unlisted) > 0 {
		r.add(Finding{
			Check:    "apps",
			Severity: SevWarn,
			Title:    "installed but not registered: " + strings.Join(unlisted, ", "),
			Detail: "These apps are in apps/ but missing from sites/apps.txt, so frappe never loads them. " +
				"Their DocTypes, hooks and scheduled jobs do nothing.",
			FixLabel: "add them to sites/apps.txt",
			fix:      func() error { return writeAppsTxt(benchPath, append(appsTxtNames(benchPath), unlisted...)) },
		})
	}

	// Registered with nothing behind it. frappe raises ModuleNotFoundError
	// out of setup_module_map() and takes the whole command group with it.
	//
	// Only when apps/ itself is intact, though. A bench whose apps/ link is
	// missing makes every registration look stale, and the repair for that is
	// to restore apps/ (checkLayout does it) -- not to delete the record of
	// what was installed.
	orphans := staleRegistrations(benchPath, listed)
	if len(orphans) > 0 {
		r.add(Finding{
			Check:    "apps",
			Severity: SevError,
			Title:    "registered but not installed: " + strings.Join(orphans, ", "),
			Detail: "sites/apps.txt names these apps but apps/ has no directory for them. frappe imports every name in " +
				"that file at startup, so this breaks bench new-site, the workers and the whole frappe command group.",
			FixLabel: "remove them from sites/apps.txt",
			// Recomputed here, not captured: every fix in this report runs
			// after the ones before it, and an earlier repair -- relinking
			// apps/ -- can make these registrations valid again. A fix that
			// deletes has to re-establish its own premise first.
			fix: func() error {
				stale := staleRegistrations(benchPath, appsTxtNames(benchPath))
				if len(stale) == 0 {
					return nil
				}
				return writeAppsTxt(benchPath, without(appsTxtNames(benchPath), stale))
			},
		})
	}

	// Per app: is its code reachable, and is it on the virtualenv's sys.path?
	for _, app := range listed {
		link := filepath.Join(benchPath, "apps", app)
		target, err := filepath.EvalSymlinks(link)
		if err != nil {
			if fileExists(link) {
				r.add(Finding{
					Check: "apps", Severity: SevError,
					Title:  "apps/" + app + " is a broken link",
					Detail: "It points at " + readlink(link) + ", which does not exist.",
				})
			}
			continue // the orphan finding above already covers a missing link
		}

		// A path under someone else's 0700 home is unreachable to the account
		// the services run as, however permissive the app's own mode is. This
		// is what an `fpm install` run as root before the HOME fix leaves
		// behind: apps/<app> -> /root/.fpm/apps/... .
		if blocker, blocked := traversalBlock(target, runtimeUID); blocked {
			src := target
			r.add(Finding{
				Check: "apps", Severity: SevError,
				Title: app + " lives where the services cannot reach it",
				Detail: "apps/" + app + " resolves to " + src + ", but " + blocker + " can only be entered by its owner, " +
					"and bench runs as " + runtimeUserName() + ". Every site operation that imports " + app + " fails with " +
					"\"permission denied\" or ModuleNotFoundError.",
				FixLabel: "copy it into the shared fpm store and relink",
				fix: func() error {
					dest, err := relocateApp(benchPath, app, src)
					if err != nil {
						return err
					}
					return writePTH(sp, app, dest)
				},
			})
			continue
		}

		pkgDir, ok := packageDir(target, app)
		if !ok {
			r.add(Finding{
				Check: "apps", Severity: SevError,
				Title:  app + " has no importable python package",
				Detail: "Neither " + filepath.Join(target, app) + " nor its src/ variant holds an __init__.py, so nothing can import it.",
			})
			continue
		}

		// The third leg: the virtualenv has to have the app's parent directory
		// on sys.path. fpm writes <app>.pth for that. A .pth left over from an
		// install into a different HOME points somewhere unreadable and the
		// import fails even though apps/<app> is perfect.
		if sp == "" {
			continue // no virtualenv to check; the import probe below still runs
		}
		want := filepath.Dir(pkgDir)
		pth := filepath.Join(sp, app+".pth")
		got, haveP := readPTH(pth)
		switch {
		case !haveP && !isDir(filepath.Join(sp, app)):
			r.add(Finding{
				Check: "apps", Severity: SevError,
				Title:    app + " is not on the virtualenv's path",
				Detail:   "sites/apps.txt registers " + app + " and apps/" + app + " exists, but " + pth + " is missing, so `import " + app + "` fails.",
				FixLabel: "write " + app + ".pth",
				fix:      func() error { return writePTH(sp, app, want) },
			})
		case haveP && !samePath(got, want):
			r.add(Finding{
				Check: "apps", Severity: SevError,
				Title: app + ".pth points at the wrong directory",
				Detail: pth + " puts " + got + " on sys.path, but " + app + " actually lives in " + want + ". " +
					"This is what an install run under a different HOME leaves behind.",
				FixLabel: "repoint it at " + want,
				fix:      func() error { return writePTH(sp, app, want) },
			})
		}
	}

	// The authoritative test, and the one frappe itself performs: ask the
	// bench's own interpreter to import each app. It catches registrations
	// broken in ways no file check anticipates.
	if probe, ok := importProbe(benchPath, listed); ok {
		var failures []string
		for _, app := range listed {
			if msg := probe[app]; msg != "" {
				failures = append(failures, app+" ("+msg+")")
			}
		}
		if len(failures) > 0 {
			r.add(Finding{
				Check: "apps", Severity: SevError,
				Title:  "these apps cannot be imported: " + strings.Join(failures, ", "),
				Detail: "frappe imports every app in sites/apps.txt when it sets up its module map. Until each one imports, bench new-site, the workers and the scheduler all fail.",
			})
		} else if len(listed) > 0 {
			r.add(Finding{Check: "apps", Severity: SevOK, Title: fmt.Sprintf("all %d registered apps import cleanly", len(listed))})
		}
	}
}

// packageDir returns the directory holding app's python package, handling both
// the flat layout fpm ships and a src/ layout.
// staleRegistrations lists the apps named in sites/apps.txt that have nothing
// in apps/ behind them. It returns nothing at all when apps/ is missing or
// unreadable: that is one broken directory, not N stale registrations, and
// treating it as the latter would delete a correct apps.txt.
func staleRegistrations(benchPath string, listed []string) []string {
	appsDir := filepath.Join(benchPath, "apps")
	if !isDir(appsDir) {
		return nil
	}
	var stale []string
	for _, app := range listed {
		if !fileExists(filepath.Join(appsDir, app)) {
			stale = append(stale, app)
		}
	}
	return stale
}

func packageDir(root, app string) (string, bool) {
	for _, c := range []string{filepath.Join(root, app), filepath.Join(root, "src", app)} {
		if fileExists(filepath.Join(c, "__init__.py")) {
			return c, true
		}
	}
	return "", false
}

// importProbe asks the bench's interpreter which apps actually import. ok is
// false when there is no usable interpreter, so the caller can stay quiet
// rather than report a failure it cannot attribute.
func importProbe(benchPath string, apps []string) (map[string]string, bool) {
	if len(apps) == 0 {
		return nil, false
	}
	py := benchPython(benchPath)
	if py == "" {
		return nil, false
	}
	const script = `
import importlib, sys
for name in sys.argv[1:]:
    try:
        importlib.import_module(name)
        print(name + "\t")
    except BaseException as e:
        print(name + "\t" + type(e).__name__ + ": " + str(e).replace("\n", " ")[:160])
`
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := runtimeCommand(benchPath, py, append([]string{"-c", script}, apps...)...)
	out, _ := runCaptured(ctx, cmd)
	res := map[string]string{}
	var seen bool
	for _, line := range strings.Split(string(out), "\n") {
		name, msg, found := strings.Cut(strings.TrimRight(line, "\r"), "\t")
		if !found || !contains(apps, name) {
			continue
		}
		seen = true
		res[name] = strings.TrimSpace(msg)
	}
	return res, seen
}

// benchPython is the interpreter bench itself uses for this bench: the
// virtualenv's, or the one in the packaged bench when env/ still links there.
func benchPython(benchPath string) string {
	var candidates []string
	for _, base := range []string{benchPath, PackagedBenchPath()} {
		if base == "" {
			continue
		}
		candidates = append(candidates,
			filepath.Join(base, "env", "bin", "python3"),
			filepath.Join(base, "env", "bin", "python"),
		)
	}
	for _, c := range candidates {
		if isExecutable(c) {
			return c
		}
	}
	return ""
}

// sitePackagesDir finds the virtualenv's site-packages, whichever python
// minor version built it.
func sitePackagesDir(benchPath string) string {
	lib := filepath.Join(benchPath, "env", "lib")
	entries, err := os.ReadDir(lib)
	if err != nil {
		return ""
	}
	var found []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "python") {
			if p := filepath.Join(lib, e.Name(), "site-packages"); isDir(p) {
				found = append(found, p)
			}
		}
	}
	if len(found) == 0 {
		return ""
	}
	sort.Strings(found) // highest python version last, and newest
	return found[len(found)-1]
}

func readPTH(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		// .pth files may carry `import ...` lines; only a bare path adds to sys.path.
		if line != "" && !strings.HasPrefix(line, "import ") && !strings.HasPrefix(line, "#") {
			return line, true
		}
	}
	return "", true
}

func writePTH(sitePackages, app, dir string) error {
	if sitePackages == "" {
		return fmt.Errorf("this bench has no virtualenv to register %s in", app)
	}
	path := filepath.Join(sitePackages, app+".pth")
	if err := daemonWriteFile(path, []byte(dir+"\n")); err != nil {
		return err
	}
	chownDaemon(path)
	return nil
}

// relocateApp copies an app out of a directory the services cannot enter into
// the shared fpm store, and repoints apps/<app> at the copy. The version tail
// of the original path is preserved so the store keeps its usual shape
// (.fpm/apps/<org>/<app>/<version>).
func relocateApp(benchPath, app, src string) (string, error) {
	store := filepath.Join(fpmConfigHome(), ".fpm", "apps")
	tail := filepath.Join("local", app)
	if _, after, found := strings.Cut(src, string(filepath.Separator)+".fpm"+string(filepath.Separator)+"apps"+string(filepath.Separator)); found {
		tail = after
	}
	dest := filepath.Join(store, tail)
	if !isDir(dest) {
		if err := daemonMkdirAll(filepath.Dir(dest)); err != nil {
			return "", err
		}
		// The copy is read from a directory only root can enter, so it is root
		// that must read it; the chown afterwards hands the result over.
		if out, err := exec.Command("cp", "-a", src, dest).CombinedOutput(); err != nil {
			if err2 := daemonCopyTree(src, dest); err2 != nil {
				return "", fmt.Errorf("copying %s to %s: %v: %s", src, dest, err, strings.TrimSpace(string(out)))
			}
		}
	}
	chownDaemonTree(dest)

	link := filepath.Join(benchPath, "apps", app)
	if fi, err := os.Lstat(link); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		if err := daemonRemove(link); err != nil {
			return "", err
		}
	}
	if !fileExists(link) {
		if err := daemonSymlink(dest, link); err != nil {
			return "", err
		}
		chownDaemonLink(link)
	}
	return dest, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Access
// ──────────────────────────────────────────────────────────────────────────────

func checkAccess(benchPath string, r *Report) {
	runtimeUID := runtimeUserID()

	// The bench itself. Under the snap the services own it; the check that
	// matters is whether the account they run as can enter and write it.
	var unreachable []string
	for _, d := range []string{"", "sites", "config", "logs", "apps"} {
		p := filepath.Join(benchPath, d)
		if !fileExists(p) {
			continue
		}
		if blocker, blocked := traversalBlock(p, runtimeUID); blocked {
			unreachable = append(unreachable, p+" (blocked by "+blocker+")")
		}
	}
	if len(unreachable) > 0 {
		r.add(Finding{
			Check: "access", Severity: SevError,
			Title:    "the bench is not reachable by " + runtimeUserName(),
			Detail:   strings.Join(unreachable, "\n") + "\nEvery bench command run by the services fails with permission denied.",
			FixLabel: "make the bench group-traversable",
			fix: func() error {
				for _, d := range []string{"", "sites", "config", "logs"} {
					p := filepath.Join(benchPath, d)
					if !fileExists(p) {
						continue
					}
					if err := os.Chmod(p, 0o775); err != nil {
						return err
					}
					chownDaemon(p)
				}
				return nil
			},
		})
	}

	// The socket directory is deliberately world-traversable so any local user
	// can connect without being added to a group; authentication still gates
	// the database. When it is not, `bench` from a plain shell cannot reach
	// MariaDB at all.
	if common := os.Getenv("SNAP_COMMON"); common != "" {
		run := filepath.Join(common, "run")
		if fi, err := os.Stat(run); err == nil && fi.Mode().Perm()&0o005 != 0o005 {
			r.add(Finding{
				Check: "access", Severity: SevWarn,
				Title:    "the database socket directory is not traversable",
				Detail:   run + " is mode " + fmt.Sprintf("%04o", fi.Mode().Perm()) + ". Local users outside the snap_daemon group cannot reach the MariaDB socket, so bench falls back to TCP or fails outright.",
				FixLabel: "chmod 0755 " + run,
				fix:      func() error { return os.Chmod(run, 0o755) },
			})
		}
	}

	// The other half of access, and the one that bites the CLI rather than the
	// services: the bench is shared between the daemons that own it and whoever
	// types a command. That sharing is made of two bits -- group write, so both
	// sides can create files, and setgid on directories, so new files keep the
	// shared group instead of quietly ending the sharing at the next write.
	// Without them `bench new-site`, an app install and doctor's own repairs all
	// fail with "permission denied" on a tree that looks perfectly healthy.
	var unshared []string
	for _, d := range []string{"", "sites", "config", "logs"} {
		p := filepath.Join(benchPath, d)
		fi, err := os.Stat(p)
		if err != nil || !fi.IsDir() {
			continue
		}
		if fi.Mode().Perm()&0o020 == 0 {
			unshared = append(unshared, p)
		}
	}
	if len(unshared) > 0 {
		r.add(Finding{
			Check: "access", Severity: SevError,
			Title: "the bench is not writable by the account that owns it",
			Detail: strings.Join(unshared, "\n") + "\nThese are not group-writable. The bench is shared between the services and " +
				"whoever types a command, and inside the snap root has no DAC override -- so a tree owned by " + runtimeUserName() +
				" and writable only by its owner is one that bench new-site, an app install and doctor's own repairs all fail on " +
				"with \"permission denied\".",
			FixLabel: "add group write",
			fix: func() error {
				for _, p := range unshared {
					if err := daemonChmod("g+rwx", p); err != nil {
						return err
					}
				}
				return nil
			},
		})
	}

	// Deliberately not checked: the setgid bit on these directories. It cannot be
	// set from inside a strict snap at all -- mkdir(2) has its setgid stripped by
	// the kernel, and snapd's seccomp policy refuses a chmod that carries
	// S_ISGID -- so a directory only ever inherits it. Reporting something no
	// action can change is noise, and it does not matter here anyway: umask 002
	// and a single service account already keep new files group-writable.

	if len(unreachable) == 0 && len(unshared) == 0 {
		r.add(Finding{Check: "access", Severity: SevOK, Title: "the bench is reachable and writable by " + runtimeUserName()})
	}
}

// checkWritableTrees reports a bench that cannot take an app install.
//
// On a production install apps/, env/ and sites/assets are symlinks into the
// package's read-only bench: immutable, and rolled back atomically by
// `snap revert`. That is the intended layout, not a fault -- but installing an
// app writes to all three (an apps/<app> link, a pip install into env/, an
// assets link), so on such a bench every install ends in "read-only file
// system" and a rollback, with nothing said about why.
//
// This is reported, never repaired: turning the bench writable copies about
// 1.5 GB, which is not something a diagnostic should do on its own.
func checkWritableTrees(benchPath string, r *Report) {
	packaged := StableBenchPath()
	if packaged == "" {
		return
	}
	var readonly []string
	for _, t := range []string{"apps", "env", filepath.Join("sites", "assets")} {
		p := filepath.Join(benchPath, t)
		fi, err := os.Lstat(p)
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			continue
		}
		if target, err := filepath.EvalSymlinks(p); err == nil && strings.HasPrefix(target, trimRevision(packaged)) {
			readonly = append(readonly, t)
		}
	}
	if len(readonly) == 0 {
		r.add(Finding{Check: "apps", Severity: SevOK, Title: "the bench is writable, so apps can be installed into it"})
		return
	}
	r.add(Finding{
		Check: "apps", Severity: SevWarn,
		Title: "no app can be installed into this bench: " + strings.Join(readonly, ", ") + " are read-only",
		Detail: "They are symlinks into the bench shipped with the package, which is a read-only filesystem. That is the normal " +
			"production layout -- the install is immutable and `snap revert` rolls it back -- but an app install has to write an " +
			"apps/<app> link, pip-install into env/ and link the app's assets, so it fails with \"read-only file system\" and rolls " +
			"back. To make this bench writable (about 1.5 GB, once): make it the active bench, run " +
			"'sudo snap set " + InstanceName() + " mode=developer', then 'sudo snap set " + InstanceName() + " mode=production' to " +
			"bring the services back. The copied trees stay.",
	})
}

// trimRevision maps /snap/<name>/current/... to the prefix both it and the
// revision-specific /snap/<name>/34/... share, because EvalSymlinks resolves
// `current` to the revision.
func trimRevision(p string) string {
	if !strings.HasPrefix(p, "/snap/") {
		return p
	}
	parts := strings.Split(strings.TrimPrefix(p, "/snap/"), "/")
	if len(parts) < 2 {
		return p
	}
	return "/snap/" + parts[0] + "/"
}

// checkBackupTools proves that the two external tools frappe shells out to for
// file backups actually work for the account bench runs as.
//
// They fail in ways that say nothing useful. Inside the snap, the base's GNU
// tar probes openat2(2), which snapd's seccomp policy answers with EPERM
// rather than ENOSYS -- tar only falls back on ENOSYS -- so every archive it
// touches dies with "Cannot stat: Operation not permitted", and frappe reports
// "Database or site_config.json may be corrupted". And `bench restore` runs
// file(1), which older snap builds do not ship at all ("file: command not
// found"), or ships without its magic database (exit 1). Both were found by
// driving the TUI on a real install; neither shows in any file mode or in the
// bench's own layout, so the only honest check is to run them.
//
// The probe archives a member two directories deep (./a/b), and that depth is
// the whole point. Measured under confinement: `tar -cf /dev/null ./a` succeeds
// and `tar -cf /dev/null ./a/b` fails, because tar opens the *parent* of the
// member with openat2 and a single-component path has no parent to open. Frappe
// archives ./<site>/public/files, so a probe of ./a would report a broken
// install as healthy -- the one thing a health check must never do.
func checkBackupTools(r *Report) {
	dir, err := os.MkdirTemp("", "vybench-doctor-")
	if err != nil {
		return // nowhere to probe from: say nothing rather than guess
	}
	defer os.RemoveAll(dir)
	nested := filepath.Join(dir, "a", "b")
	sample := filepath.Join(nested, "f.txt")
	// World-readable: inside the snap this runs as root but the tools run as
	// snap_daemon, which must be able to read what root made.
	if os.MkdirAll(nested, 0o755) != nil || os.Chmod(dir, 0o755) != nil ||
		os.Chmod(filepath.Join(dir, "a"), 0o755) != nil || os.WriteFile(sample, []byte("x\n"), 0o644) != nil {
		return
	}

	probe := func(name string, args ...string) string {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cmd := runtimeCommand(dir, name, args...)
		if cmd.Err != nil { // not on PATH
			return name + ": not found on PATH"
		}
		out, err := runCaptured(ctx, cmd)
		if err == nil {
			return ""
		}
		// The FIRST line, not the last: tar ends with a generic "Exiting with
		// failure status due to previous errors" after the line that names the
		// actual cause.
		for _, l := range strings.Split(string(out), "\n") {
			if l = strings.TrimSpace(sanitize(l)); l != "" {
				return l
			}
		}
		return name + ": " + err.Error()
	}

	var problems []string
	if msg := probe("tar", "-cf", "/dev/null", "./a/b"); msg != "" {
		problems = append(problems, "tar cannot archive a directory: "+msg)
	}
	if msg := probe("file", sample); msg != "" {
		problems = append(problems, "file cannot identify a file: "+msg)
	}
	if len(problems) == 0 {
		r.add(Finding{Check: "backup", Severity: SevOK, Title: "backups that include files can be made and restored (tar and file work)"})
		return
	}
	r.add(Finding{
		Check: "backup", Severity: SevError,
		Title: "backups that include files cannot be made or restored on this install",
		Detail: strings.Join(problems, "\n") + "\nFrappe shells out to tar for `bench backup --with-files` and to tar and file for `bench restore`, " +
			"and then blames the database (\"may be corrupted\"), which is not what is wrong. Database-only backups are unaffected. " +
			"This is a packaging fault, so it cannot be repaired in place: refresh " + InstanceName() + " to a build that ships its own " +
			"tar and file.",
	})
}

// ──────────────────────────────────────────────────────────────────────────────
// fpm store
// ──────────────────────────────────────────────────────────────────────────────

// defaultFPMConfig is what fpm-wrapper seeds. fpm ships with no repository at
// all, so without this every install ends in "no repositories configured".
func defaultFPMConfig(registry string) []byte {
	cfg := map[string]any{"repositories": map[string]any{
		"vyogo": map[string]any{"name": "vyogo", "url": registry, "priority": 0},
	}}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	return append(data, '\n')
}

func checkFPMStore(r *Report) {
	path := fpmConfigPath()
	registry := os.Getenv("VYBENCH_FPM_REGISTRY")
	if registry == "" {
		registry = DefaultRegistryURL
	}

	seed := Finding{
		Check: "fpm", Severity: SevError,
		FixLabel: "write " + path + " with the vyogo repository",
		fix: func() error {
			if err := daemonMkdirAll(filepath.Dir(path)); err != nil {
				return err
			}
			if err := daemonWriteFile(path, defaultFPMConfig(registry)); err != nil {
				return err
			}
			chownDaemonTree(filepath.Dir(path))
			return nil
		},
	}

	data, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		seed.Title = "fpm has no repository configured"
		seed.Detail = path + " does not exist, so every `fpm install` fails with \"no repositories configured\" and the marketplace can install nothing."
		r.add(seed)
		return
	case err != nil:
		seed.Title = "fpm's configuration cannot be read"
		seed.Detail = err.Error() + "\nUnder strict confinement root has no DAC override, so a config left in a private home is unreadable to everything that matters."
		r.add(seed)
		return
	}
	var cfg struct {
		Repositories map[string]struct {
			URL string `json:"url"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil || len(cfg.Repositories) == 0 {
		seed.Title = "fpm has no usable repository"
		seed.Detail = path + " lists no repository, so package installs and the marketplace have nowhere to fetch from."
		r.add(seed)
		return
	}
	r.add(Finding{Check: "fpm", Severity: SevOK, Title: fmt.Sprintf("fpm has %d repository(ies) configured", len(cfg.Repositories))})
}

// ──────────────────────────────────────────────────────────────────────────────
// Sites
// ──────────────────────────────────────────────────────────────────────────────

func checkSites(benchPath string, r *Report) {
	entries, err := os.ReadDir(filepath.Join(benchPath, "sites"))
	if err != nil {
		return
	}
	healthy := 0
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || name == "assets" || strings.HasPrefix(name, ".") {
			continue
		}
		if !fileExists(filepath.Join(benchPath, "sites", name, "site_config.json")) {
			continue // not a site directory
		}
		check, decided := QuickSiteCheck(benchPath, name)
		if !decided || check.State != SiteIncomplete {
			healthy++
			continue
		}
		site := name
		r.add(Finding{
			Check: "sites", Severity: SevWarn,
			Title: site + " is left over from an installation that did not finish",
			Detail: check.Reason + ". The site directory holds credentials for a database that is not there, so anything that touches it " +
				"fails with \"Access denied\" or \"Unknown database\", and the name cannot be reused.",
			FixLabel: "move it to archived/sites",
			fix: func() error {
				// Re-checked at the moment of the move, not when the report was
				// built: this one deletes nothing, but it does take a site out
				// of service, and an earlier repair in the same run may have
				// made it whole.
				if again, decided := QuickSiteCheck(benchPath, site); decided && again.State != SiteIncomplete {
					return nil
				}
				dest, err := archiveSite(benchPath, site)
				if err != nil {
					return err
				}
				chownDaemonTree(dest)
				return nil
			},
		})
	}
	if healthy > 0 {
		r.add(Finding{Check: "sites", Severity: SevOK, Title: fmt.Sprintf("%d site(s) look complete", healthy)})
	}
}

func checkDBRoot(benchPath string, r *Report) {
	cfg, err := readConfig(commonConfigPath(benchPath))
	if err != nil {
		r.add(Finding{
			Check: "database", Severity: SevError,
			Title:  "sites/common_site_config.json is missing or unreadable",
			Detail: err.Error() + "\nbench reads this file for the database host, port and socket; without it no site can be created or served.",
		})
		return
	}
	engine, _ := cfg["db_type"].(string)
	if engine == "" {
		engine = "mariadb"
	}
	if engine != "mariadb" {
		return
	}
	if ResolveMariaDBRootPassword(benchPath, "") == "" {
		r.add(Finding{
			Check: "database", Severity: SevWarn,
			Title:  "the MariaDB root password cannot be found",
			Detail: "bench new-site needs it to create a site's database and user. Without it the command stops to prompt, which in the TUI or a script looks like a hang.",
		})
		return
	}
	r.add(Finding{Check: "database", Severity: SevOK, Title: "the MariaDB root password is available to bench"})
}

func checkRegistry(ctx context.Context, r *Report) {
	c := NewFPMClient()
	// Long enough for the retry rounds in fpm_client.go to actually run; short
	// enough that a diagnostic still finishes.
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	cat := c.FetchCatalog(ctx)
	if cat.Offline {
		r.add(Finding{
			Check: "registry", Severity: SevWarn,
			Title: "the package registry could not be read",
			Detail: fmt.Sprintf("%v\nThe marketplace falls back to a built-in list, which carries no versions, so apps show as \"latest\" "+
				"and an install has to resolve the version itself. Every attempt was retried, so this is the network path to the "+
				"registry rather than a passing glitch. Set VYBENCH_FPM_REGISTRY to a comma-separated list to add a mirror that is "+
				"reachable from here.", cat.Err),
		})
		return
	}
	r.add(Finding{Check: "registry", Severity: SevOK, Title: fmt.Sprintf("the registry lists %d packages", len(cat.Packages))})
}

// ──────────────────────────────────────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────────────────────────────────────

func appsTxtNames(benchPath string) []string {
	data, err := os.ReadFile(filepath.Join(benchPath, "sites", "apps.txt"))
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if n := strings.TrimSpace(line); n != "" && !contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

// writeAppsTxt rewrites the registration list, keeping frappe first: frappe
// itself relies on being the first app loaded.
func writeAppsTxt(benchPath string, names []string) error {
	var out []string
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" && !contains(out, n) {
			out = append(out, n)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i] == "frappe" && out[j] != "frappe" })
	path := filepath.Join(benchPath, "sites", "apps.txt")
	if err := daemonWriteFile(path, []byte(strings.Join(out, "\n")+"\n")); err != nil {
		return err
	}
	chownDaemon(path)
	return nil
}

func appsOnDisk(benchPath string) []string {
	appsDir := filepath.Join(benchPath, "apps")
	entries, err := os.ReadDir(appsDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, ".") || n == "README.md" {
			continue
		}
		full := filepath.Join(appsDir, n)
		if !isDir(full) {
			continue
		}
		// A symlink named "apps" inside apps/ is a stale bootstrap artifact
		// (apps -> /snap/.../apps); skip it rather than treating the parent
		// directory as an app.
		if n == "apps" {
			if target, err := os.Readlink(full); err == nil {
				resolved, _ := filepath.EvalSymlinks(full)
				if resolved == appsDir || strings.HasSuffix(target, "/apps") {
					continue
				}
			}
		}
		out = append(out, n)
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func without(list, drop []string) []string {
	var out []string
	for _, x := range list {
		if !contains(drop, x) {
			out = append(out, x)
		}
	}
	return out
}

func samePath(a, b string) bool {
	clean := func(s string) string { return strings.TrimRight(filepath.Clean(strings.TrimSpace(s)), "/") }
	if clean(a) == clean(b) {
		return true
	}
	ra, ea := filepath.EvalSymlinks(a)
	rb, eb := filepath.EvalSymlinks(b)
	return ea == nil && eb == nil && clean(ra) == clean(rb)
}

func readlink(path string) string {
	t, err := os.Readlink(path)
	if err != nil {
		return path
	}
	return t
}

// runtimeUserName is the account bench processes actually run as: snap_daemon
// inside the snap, the invoking user everywhere else.
func runtimeUserName() string {
	if DetectPlatform() == PlatformSnap {
		return "snap_daemon"
	}
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return "this user"
}

// runtimeUserID is that account's uid, or -1 when it cannot be resolved.
func runtimeUserID() int {
	if DetectPlatform() == PlatformSnap {
		u, err := user.Lookup("snap_daemon")
		if err != nil {
			return -1
		}
		id, err := strconv.Atoi(u.Uid)
		if err != nil {
			return -1
		}
		return id
	}
	return os.Getuid()
}

// runtimeCommand builds a command that runs as the account bench runs as.
// Inside the snap, root must drop to snap_daemon or it reads the bench with
// the wrong identity -- and with CAP_DAC_OVERRIDE gone, often cannot read it
// at all.
func runtimeCommand(dir, name string, args ...string) *exec.Cmd {
	var cmd *exec.Cmd
	if DetectPlatform() == PlatformSnap && os.Geteuid() == 0 {
		full := append([]string{"--reuid=snap_daemon", "--regid=snap_daemon", "--clear-groups", name}, args...)
		cmd = exec.Command("setpriv", full...)
	} else {
		cmd = exec.Command(name, args...)
	}
	cmd.Dir = dir
	cmd.Env = benchEnv(dir)
	return cmd
}

// The bench tree belongs to snap_daemon, and inside a strict snap root has no
// CAP_DAC_OVERRIDE -- so a directory owned by snap_daemon and not group-writable
// is one root cannot create a file in, however privileged the process looks.
// Every repair that touches the bench therefore does its filesystem work as the
// account that owns it, exactly as snap-common.sh's as_daemon does. Off the
// snap, the bench belongs to the invoking user and these are plain Go calls.
func asDaemon() bool { return DetectPlatform() == PlatformSnap && os.Geteuid() == 0 }

func daemonRun(name string, args ...string) error {
	cmd := runtimeCommand("/", name, args...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	// Name the identity the command actually ran under. A permission error on
	// a bench is almost always about who ran the command, not about the mode
	// bits, and that is the part the message usually leaves out.
	who := fmt.Sprintf("as uid %d/gid %d", os.Geteuid(), os.Getegid())
	if cmd.Path != "" && strings.HasSuffix(cmd.Path, "setpriv") {
		who = "as " + runtimeUserName()
	}
	if msg := strings.TrimSpace(string(out)); msg != "" {
		return fmt.Errorf("%s %s: %s", strings.Join(cmd.Args, " "), who, msg)
	}
	return fmt.Errorf("%s %s: %w", strings.Join(cmd.Args, " "), who, err)
}

func daemonMkdirAll(path string) error {
	if !asDaemon() {
		return os.MkdirAll(path, 0o775)
	}
	return daemonRun("mkdir", "-p", path)
}

func daemonRename(src, dst string) error {
	if !asDaemon() {
		return os.Rename(src, dst)
	}
	return daemonRun("mv", src, dst)
}

func daemonRemove(path string) error {
	if !asDaemon() {
		return os.Remove(path)
	}
	return daemonRun("rm", "-f", path)
}

func daemonSymlink(target, link string) error {
	if !asDaemon() {
		return os.Symlink(target, link)
	}
	return daemonRun("ln", "-sfn", target, link)
}

// daemonCopyTree uses cp -a rather than a Go walk: app trees carry symlinks
// (sites/assets targets, node_modules) that have to survive as symlinks.
func daemonCopyTree(src, dst string) error {
	return daemonRun("cp", "-a", src, dst)
}

// daemonChmod takes a symbolic mode, so a fix can add the sharing bits without
// having to decide what the rest of the mode should be.
func daemonChmod(mode, path string) error { return daemonRun("chmod", mode, path) }

func daemonWriteFile(path string, data []byte) error {
	if !asDaemon() {
		return os.WriteFile(path, data, 0o664)
	}
	cmd := runtimeCommand(filepath.Dir(path), "tee", path)
	cmd.Stdin = strings.NewReader(string(data))
	out, err := cmd.Output()
	_ = out
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// chownDaemon hands a path to snap_daemon when the snap's services own the
// bench and we are root. Everywhere else the bench belongs to the invoking
// user already and this is a no-op.
func chownDaemon(path string) {
	uid, gid, ok := daemonIDs()
	if !ok {
		return
	}
	_ = os.Chown(path, uid, gid)
}

func chownDaemonLink(path string) {
	uid, gid, ok := daemonIDs()
	if !ok {
		return
	}
	_ = os.Lchown(path, uid, gid)
}

func chownDaemonTree(root string) {
	if _, _, ok := daemonIDs(); !ok {
		return
	}
	_ = filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			chownDaemonLink(p)
			return nil
		}
		chownDaemon(p)
		return nil
	})
}

func daemonAccount() (string, bool) {
	switch DetectPlatform() {
	case PlatformSnap:
		return "snap_daemon", true
	case PlatformNative:
		return "frappe", true
	default:
		return "", false
	}
}

func daemonIDs() (int, int, bool) {
	name, ok := daemonAccount()
	if !ok || os.Geteuid() != 0 {
		return 0, 0, false
	}
	u, err := user.Lookup(name)
	if err != nil {
		return 0, 0, false
	}
	uid, err1 := strconv.Atoi(u.Uid)
	gid, err2 := strconv.Atoi(u.Gid)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return uid, gid, true
}

// traversalBlock returns the first ancestor of path that only its owner may
// enter, when that owner is not the account bench runs as.
//
// This is the check mode bits alone cannot make: an app directory can be 0777
// and still be invisible to the services because /root above it is 0700. Group
// access is treated as sufficient -- whether the runtime user is in that group
// is not cheap to establish here, and the import probe settles it either way.
func traversalBlock(path string, runtimeUID int) (string, bool) {
	if runtimeUID < 0 || path == "" {
		return "", false
	}
	var chain []string
	for p := filepath.Clean(path); ; {
		chain = append(chain, p)
		parent := filepath.Dir(p)
		if parent == p {
			break
		}
		p = parent
	}
	for i := len(chain) - 1; i >= 0; i-- {
		fi, err := os.Stat(chain[i])
		if err != nil || !fi.IsDir() {
			continue
		}
		perm := fi.Mode().Perm()
		if perm&0o005 == 0o005 || perm&0o050 == 0o050 {
			continue // world- or group-traversable
		}
		owner, ok := fileOwnerUID(fi)
		if !ok || owner == runtimeUID {
			continue
		}
		return chain[i], true
	}
	return "", false
}
