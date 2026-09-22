package core

// Site operations. Each runs `bench …` through BenchCommand, so the platform
// wrapper supplies the MariaDB root credentials exactly as it does for a
// hand-typed command.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

// NewSiteOptions holds the inputs of `bench new-site`.
type NewSiteOptions struct {
	Name           string
	AdminPassword  string
	DBEngine       string // "mariadb" or "postgres"
	DBRootUser     string // PostgreSQL only; defaults to "postgres"
	DBRootPassword string // PostgreSQL only
	// Force replaces an existing site of this name (bench new-site --force).
	// Its database name is reused, so frappe drops and recreates that
	// database instead of leaving it behind.
	Force bool
	// BackupFirst backs the existing site up before replacing it.
	BackupFirst bool
	// InstallApps are bench apps to install on the new site (--install-app).
	InstallApps []string
}

var siteNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)

// ValidateSiteName checks that name is a lowercase host name.
func ValidateSiteName(name string) error {
	if len(name) > 253 || !siteNamePattern.MatchString(name) {
		return fmt.Errorf("invalid site name %q: use a lowercase host name such as mysite.localhost", name)
	}
	return nil
}

// SiteExists reports whether sites/<site> exists on the bench.
func SiteExists(benchPath, site string) bool {
	return fileExists(filepath.Join(benchPath, "sites", site))
}

// ErrSiteExists is returned when a site name is taken and Force is off.
var ErrSiteExists = errors.New("site already exists")

// NewSiteSpec returns the job that creates, or with Force replaces, a site.
func NewSiteSpec(benchPath string, o NewSiteOptions) (JobSpec, error) {
	o.Name = strings.TrimSpace(o.Name)
	if err := ValidateSiteName(o.Name); err != nil {
		return JobSpec{}, err
	}
	exists := SiteExists(benchPath, o.Name)
	if exists && !o.Force {
		return JobSpec{}, fmt.Errorf("%w: %s", ErrSiteExists, o.Name)
	}
	if o.AdminPassword == "" {
		return JobSpec{}, errors.New("the admin password cannot be empty")
	}
	engine, err := NormalizeDBEngine(o.DBEngine)
	if err != nil {
		return JobSpec{}, err
	}
	args := []string{"new-site", o.Name, "--admin-password", o.AdminPassword, "--db-type", engine}
	if engine == "postgres" {
		user := strings.TrimSpace(o.DBRootUser)
		if user == "" {
			user = "postgres"
		}
		args = append(args, "--db-root-username", user)
		if o.DBRootPassword != "" {
			args = append(args, "--db-root-password", o.DBRootPassword)
		}
	} else if engine == "mariadb" {
		if pw := ResolveMariaDBRootPassword(benchPath, o.DBRootPassword); pw != "" {
			args = append(args, "--mariadb-root-password", pw)
		}
		if sock := resolveMariaDBSocket(benchPath); sock != "" {
			args = append(args, "--db-socket", sock)
		}
	}
	for _, app := range o.InstallApps {
		args = append(args, "--install-app", app)
	}
	label := fmt.Sprintf("Creating site %s (%s)", o.Name, engine)
	if len(o.InstallApps) > 0 {
		label += " with " + strings.Join(o.InstallApps, ", ")
	}
	var steps []Step
	if exists {
		args = append(args, "--force")
		if db, _ := siteDBName(benchPath, o.Name); db != "" {
			args = append(args, "--db-name", db)
		}
		label = fmt.Sprintf("Replacing site %s (%s)", o.Name, engine)
		if o.BackupFirst {
			backup, err := BackupSpec(benchPath, o.Name)
			if err != nil {
				return JobSpec{}, err
			}
			steps = append(steps, backup.Steps...)
		}
	}
	cmd, err := BenchCommand(benchPath, args...)
	if err != nil {
		return JobSpec{}, err
	}
	return JobSpec{Steps: append(steps, Step{Label: label, Cmd: cmd})}, nil
}

// InstallAppSpec returns the job that installs a bench app on a site.
func InstallAppSpec(benchPath, site, app string) (JobSpec, error) {
	return benchJob(benchPath, "Installing "+app+" on "+site, "--site", site, "install-app", app)
}

// BenchAppNames lists the apps in the bench that can go on a site: every app
// in sites/apps.txt except frappe, which every site has.
func BenchAppNames(benchPath string) []string {
	var names []string
	for app := range InstalledApps(benchPath) {
		if app != "frappe" {
			names = append(names, app)
		}
	}
	sort.Strings(names)
	return names
}

// BackupSpec returns the job that backs up a site's database and files.
func BackupSpec(benchPath, site string) (JobSpec, error) {
	return benchJob(benchPath, "Backing up "+site, "--site", site, "backup", "--with-files")
}

// DropSiteSpec returns the job that drops a site. bench backs it up and moves
// its folder to archived/sites/. Force carries on when that backup fails.
// A site whose database was never created cannot be dropped by bench, which
// connects to the database first, so its folder is archived directly.
func DropSiteSpec(benchPath, site string, force bool, check SiteCheck) (JobSpec, error) {
	if check.State == SiteIncomplete && check.DBMissing {
		return JobSpec{Steps: []Step{{
			Label: "Archiving " + site + " (its database was never created, so there is nothing to back up)",
			Fn: func(log func(string)) error {
				dest, err := archiveSite(benchPath, site)
				if err == nil {
					log("Moved to " + dest)
				}
				return err
			},
		}}}, nil
	}
	args := []string{"drop-site", site}
	if pw := ResolveMariaDBRootPassword(benchPath, ""); pw != "" {
		args = append(args, "--mariadb-root-password", pw)
	}
	// No --db-socket: `bench drop-site` does not accept it (only new-site does),
	// so passing it made every drop fail with "No such option" before touching
	// anything. It reads the socket from common_site_config.json. Pinned per
	// subcommand by TestSpecFlagsExistInRealBench.
	label := "Dropping " + site + " (bench backs it up first)"
	if force {
		args = append(args, "--force")
		label = "Dropping " + site + " (continuing even if its backup fails)"
	}
	return benchJob(benchPath, label, args...)
}

// archiveSite moves sites/<site> to archived/sites/<site>-<time>, where
// bench drop-site puts the sites it removes.
// archiveSite moves a site directory aside. The move is made by the account
// that owns the bench: inside a strict snap the tree belongs to snap_daemon and
// root cannot create archived/ in it, because root there has no DAC override.
func archiveSite(benchPath, site string) (string, error) {
	dir := filepath.Join(benchPath, "archived", "sites")
	if err := daemonMkdirAll(dir); err != nil {
		return "", err
	}
	dest := filepath.Join(dir, site+"-"+time.Now().Format("20060102_150405"))
	return dest, daemonRename(filepath.Join(benchPath, "sites", site), dest)
}

func benchJob(benchPath, label string, args ...string) (JobSpec, error) {
	cmd, err := BenchCommand(benchPath, args...)
	if err != nil {
		return JobSpec{}, err
	}
	return JobSpec{Steps: []Step{{Label: label, Cmd: cmd}}}, nil
}

// SiteURL is where the bench's web server serves site.
func SiteURL(benchPath, site string) string {
	return fmt.Sprintf("http://%s:%d", site, WebserverPort(benchPath))
}

// BackupDir is where `bench backup` writes a site's backups.
func BackupDir(benchPath, site string) string {
	return filepath.Join(benchPath, "sites", site, "private", "backups")
}

// ──────────────────────────────────────────────────────────────────────────────
// Backups and restore
// ──────────────────────────────────────────────────────────────────────────────

// Backup is one `bench backup` run: files sharing a timestamp prefix.
type Backup struct {
	Stamp        string // 20260911_124012
	Time         time.Time
	Database     string // …-database.sql.gz
	PublicFiles  string // …-files.tar or .tgz
	PrivateFiles string // …-private-files.tar or .tgz
	Encrypted    bool
	Safety       bool  // automatic copy taken just before a restore
	Size         int64 // database dump size
}

// HasFiles reports whether the backup includes the site's files.
func (b Backup) HasFiles() bool { return b.PublicFiles != "" || b.PrivateFiles != "" }

// Label describes the backup for a picker.
func (b Backup) Label() string {
	s := b.Time.Format("2006-01-02 15:04") + " · " + humanSize(b.Size)
	if b.HasFiles() {
		s += " · with files"
	}
	if b.Encrypted {
		s += " · encrypted"
	}
	if b.Safety {
		s += " · safety copy"
	}
	return s
}

// PreferredBackupIndex is the backup a restore should start on. ListBackups
// is newest-first, and the newest file is often the safety copy "Back up
// first" just wrote, which is the site as it is now rather than the copy the
// user meant to restore. Prefer the newest backup that has files and is not
// a safety copy.
func PreferredBackupIndex(backups []Backup) int {
	for i, b := range backups {
		if !b.Safety && b.HasFiles() {
			return i
		}
	}
	for i, b := range backups {
		if !b.Safety {
			return i
		}
	}
	return 0
}

func safetyStampFile(benchPath, site string) string {
	return filepath.Join(BackupDir(benchPath, site), ".vybench-safety")
}

func loadSafetyStamps(benchPath, site string) map[string]bool {
	out := map[string]bool{}
	b, err := os.ReadFile(safetyStampFile(benchPath, site))
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out[line] = true
		}
	}
	return out
}

func backupStamps(benchPath, site string) map[string]bool {
	out := map[string]bool{}
	for _, b := range ListBackups(benchPath, site) {
		out[b.Stamp] = true
	}
	return out
}

// markNewSafetyBackups records backups that appeared after before, so the
// restore picker does not offer that automatic copy as the one to restore.
func markNewSafetyBackups(benchPath, site string, before map[string]bool) error {
	var fresh []string
	for _, b := range ListBackups(benchPath, site) {
		if !before[b.Stamp] {
			fresh = append(fresh, b.Stamp)
		}
	}
	if len(fresh) == 0 {
		return nil
	}
	known := loadSafetyStamps(benchPath, site)
	var lines []string
	for stamp := range known {
		lines = append(lines, stamp)
	}
	for _, stamp := range fresh {
		if !known[stamp] {
			lines = append(lines, stamp)
		}
	}
	sort.Strings(lines)
	return os.WriteFile(safetyStampFile(benchPath, site), []byte(strings.Join(lines, "\n")+"\n"), 0o640)
}

var backupName = regexp.MustCompile(`^(\d{8}_\d{6})-.+?(-partial)?-(database|files|private-files|site_config_backup)(-enc)?\.(sql\.gz|tar|tgz|json)$`)

// ListBackups returns the site's backups, newest first. Partial backups are
// skipped: bench cannot restore a whole site from them.
func ListBackups(benchPath, site string) []Backup {
	entries, err := os.ReadDir(BackupDir(benchPath, site))
	if err != nil {
		return nil
	}
	safety := loadSafetyStamps(benchPath, site)
	byStamp := map[string]*Backup{}
	for _, e := range entries {
		m := backupName.FindStringSubmatch(e.Name())
		if m == nil || m[2] != "" {
			continue
		}
		b := byStamp[m[1]]
		if b == nil {
			t, _ := time.ParseInLocation("20060102_150405", m[1], time.Local)
			b = &Backup{Stamp: m[1], Time: t, Safety: safety[m[1]]}
			byStamp[m[1]] = b
		}
		path := filepath.Join(BackupDir(benchPath, site), e.Name())
		switch m[3] {
		case "database":
			b.Database = path
			b.Encrypted = m[4] != ""
			if fi, err := e.Info(); err == nil {
				b.Size = fi.Size()
			}
		case "files":
			b.PublicFiles = path
		case "private-files":
			b.PrivateFiles = path
		}
	}
	var out []Backup
	for _, b := range byStamp {
		if b.Database != "" {
			out = append(out, *b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Stamp > out[j].Stamp })
	return out
}

// FilesNextTo finds the public and private file archives that `bench backup`
// wrote next to a database dump, e.g. one copied over from another machine.
func FilesNextTo(sqlPath string) (public, private string) {
	dir, name := filepath.Split(sqlPath)
	m := backupName.FindStringSubmatch(name)
	i := strings.LastIndex(name, "-database")
	if m == nil || m[3] != "database" || i < 0 {
		return "", ""
	}
	matches, _ := filepath.Glob(filepath.Join(dir, name[:i]+"-*")) // "<stamp>-<site>-*"
	for _, p := range matches {
		f := backupName.FindStringSubmatch(filepath.Base(p))
		if f == nil || f[2] != "" {
			continue
		}
		switch f[3] {
		case "files":
			public = p
		case "private-files":
			private = p
		}
	}
	return public, private
}

// RestoreOptions holds the inputs of `bench --site <site> restore`.
type RestoreOptions struct {
	Site           string // an existing site is overwritten; a new name creates the site
	SQLPath        string
	PublicFiles    string
	PrivateFiles   string
	Force          bool // restore a backup from an older Frappe version, or one failing validation
	BackupFirst    bool // back the existing site up first
	DBRootUser     string
	DBRootPassword string
}

// RestoreSpec returns the job that restores a backup into a site.
func RestoreSpec(benchPath string, o RestoreOptions) (JobSpec, error) {
	o.Site = strings.TrimSpace(o.Site)
	if err := ValidateSiteName(o.Site); err != nil {
		return JobSpec{}, err
	}
	sql, err := ExpandHome(strings.TrimSpace(o.SQLPath))
	if err != nil {
		return JobSpec{}, err
	}
	if sql == "" {
		return JobSpec{}, errors.New("choose a backup or enter the path of a .sql.gz file")
	}
	if !fileExists(sql) {
		return JobSpec{}, fmt.Errorf("%s does not exist", sql)
	}
	abs, _ := filepath.Abs(sql)
	args := []string{"--site", o.Site, "restore", abs}
	for _, f := range []struct{ flag, path string }{
		{"--with-public-files", o.PublicFiles}, {"--with-private-files", o.PrivateFiles},
	} {
		if f.path != "" {
			a, _ := filepath.Abs(f.path)
			args = append(args, f.flag, a)
		}
	}
	if o.Force {
		args = append(args, "--force")
	}
	if o.DBRootUser != "" {
		args = append(args, "--db-root-username", o.DBRootUser)
		if o.DBRootPassword != "" {
			args = append(args, "--db-root-password", o.DBRootPassword)
		}
	} else if o.DBRootPassword != "" {
		args = append(args, "--mariadb-root-password", o.DBRootPassword)
	} else if pw := ResolveMariaDBRootPassword(benchPath, ""); pw != "" {
		args = append(args, "--mariadb-root-password", pw)
	}
	// No --db-socket here, unlike new-site: `bench restore` does not accept it
	// ("Error: No such option '--db-socket'"), so passing it made every restore
	// on a socket-based install (the snap, Homebrew) fail after its safety
	// backup had already run. restore takes the socket from the bench's
	// common_site_config.json, which vybench writes db_socket into.
	var steps []Step
	if o.BackupFirst && SiteExists(benchPath, o.Site) {
		backup, err := BackupSpec(benchPath, o.Site)
		if err != nil {
			return JobSpec{}, err
		}
		before := backupStamps(benchPath, o.Site)
		steps = append(steps, backup.Steps...)
		site := o.Site
		steps = append(steps, Step{
			Label: "Marking that backup as a safety copy",
			Fn: func(func(string)) error {
				return markNewSafetyBackups(benchPath, site, before)
			},
		})
	}
	cmd, err := BenchCommand(benchPath, args...)
	if err != nil {
		return JobSpec{}, err
	}
	return JobSpec{Steps: append(steps, Step{Label: "Restoring " + filepath.Base(abs) + " into " + o.Site, Cmd: cmd})}, nil
}

// ExpandHome expands a leading ~ to the home directory.
func ExpandHome(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, strings.TrimPrefix(p, "~")), nil
	}
	return p, nil
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func resolveMariaDBRootPassword(benchPath string, explicit string) string {
	return ResolveMariaDBRootPassword(benchPath, explicit)
}

// Snap paths are only candidates on a snap install. A native or Homebrew
// bench must not read another machine's leftover /var/snap tree.
var (
	snapMariaDBRootPassword = "/var/snap/vybench/common/mariadb/root_password"
	snapMariaDBSocket       = "/var/snap/vybench/common/run/mysql.sock"
)

func ResolveMariaDBRootPassword(benchPath string, explicit string) string {
	if explicit = strings.TrimSpace(explicit); explicit != "" {
		return explicit
	}
	snapCommon := os.Getenv("SNAP_COMMON")
	candidates := []string{}
	if snapCommon != "" {
		candidates = append(candidates, filepath.Join(snapCommon, "mariadb", "root_password"))
	}
	candidates = append(candidates, filepath.Join(benchPath, "config", "mariadb_root_password"))
	if DetectPlatform() == PlatformSnap {
		candidates = append(candidates, snapMariaDBRootPassword)
	}
	for _, p := range candidates {
		if data, err := os.ReadFile(p); err == nil {
			if pw := strings.TrimSpace(string(data)); pw != "" {
				return pw
			}
		}
		if runtime.GOOS == "linux" && os.Geteuid() == 0 {
			if out, err := exec.Command("setpriv", "--reuid=snap_daemon", "--regid=snap_daemon", "--clear-groups", "cat", p).Output(); err == nil {
				if pw := strings.TrimSpace(string(out)); pw != "" {
					return pw
				}
			}
		}
	}
	for _, credFile := range []string{
		"/root/.vybench_credentials",
		filepath.Join(os.Getenv("HOME"), ".vybench_credentials"),
	} {
		if data, err := os.ReadFile(credFile); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(line, "MariaDB Root Password:") {
					parts := strings.SplitN(line, ":", 2)
					if len(parts) == 2 && strings.TrimSpace(parts[1]) != "" {
						return strings.TrimSpace(parts[1])
					}
				}
			}
		}
	}
	return ""
}

func resolveMariaDBSocket(benchPath string) string {
	snapCommon := os.Getenv("SNAP_COMMON")
	candidates := []string{}
	if snapCommon != "" {
		candidates = append(candidates, filepath.Join(snapCommon, "run", "mysql.sock"))
	}
	if DetectPlatform() == PlatformSnap {
		candidates = append(candidates, snapMariaDBSocket)
	}
	for _, p := range candidates {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	cfgPath := commonConfigPath(benchPath)
	if cfg, err := readConfig(cfgPath); err == nil {
		if v, ok := cfg["db_socket"]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

type AddDomainOptions struct {
	Site              string
	Domain            string
	SSLCertificate    string
	SSLCertificateKey string
}

func ValidateDomainName(domain string) error {
	if domain == "" {
		return errors.New("domain cannot be empty")
	}
	if strings.ContainsAny(domain, " /\\") {
		return errors.New("domain cannot contain spaces or path separators")
	}
	if !siteNamePattern.MatchString(domain) {
		return fmt.Errorf("invalid domain name %q: use a valid lowercase domain format", domain)
	}
	return nil
}

func SiteDomains(benchPath, site string) []string {
	data, err := os.ReadFile(filepath.Join(benchPath, "sites", site, "site_config.json"))
	if err != nil {
		return nil
	}
	var config struct {
		Domains []interface{} `json:"domains"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return nil
	}
	var out []string
	for _, v := range config.Domains {
		switch d := v.(type) {
		case string:
			out = append(out, d)
		case map[string]interface{}:
			if ds, ok := d["domain"].(string); ok {
				out = append(out, ds)
			}
		}
	}
	sort.Strings(out)
	return out
}

func AddDomainSpec(benchPath string, opts AddDomainOptions) (JobSpec, error) {
	opts.Site = strings.TrimSpace(opts.Site)
	opts.Domain = strings.TrimSpace(opts.Domain)
	if err := ValidateSiteName(opts.Site); err != nil {
		return JobSpec{}, err
	}
	if err := ValidateDomainName(opts.Domain); err != nil {
		return JobSpec{}, err
	}
	args := []string{"setup", "add-domain", opts.Domain, "--site", opts.Site}
	if opts.SSLCertificate != "" {
		args = append(args, "--ssl-certificate", opts.SSLCertificate)
	}
	if opts.SSLCertificateKey != "" {
		args = append(args, "--ssl-certificate-key", opts.SSLCertificateKey)
	}

	cmd, err := BenchCommand(benchPath, args...)
	if err != nil {
		return JobSpec{}, err
	}
	label := fmt.Sprintf("Adding custom domain %s to %s", opts.Domain, opts.Site)
	return JobSpec{Steps: []Step{{Label: label, Cmd: cmd}}}, nil
}
