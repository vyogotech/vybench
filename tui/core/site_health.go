package core

// Site health: telling a site whose installation failed apart from a working
// one. `bench new-site` only rolls back a failed install when it can ask on a
// terminal, so a run from the TUI (or a script) leaves the half-made site
// behind, and frappe then refuses the name with "already exists".

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// SiteState classifies an existing site directory.
type SiteState int

const (
	SiteUnknown    SiteState = iota // could not tell: database down, credentials, timeout
	SiteHealthy                     // frappe is installed and the database answers
	SiteIncomplete                  // an installation that did not finish
)

// SiteCheck is the verdict on one site.
type SiteCheck struct {
	State     SiteState
	Reason    string   // one line for the UI
	DBMissing bool     // its database was never created, so bench cannot connect to drop it
	Apps      []string // apps installed on a healthy site
}

// siteDBName reads db_name from the site's site_config.json.
func siteDBName(benchPath, site string) (string, error) {
	cfg, err := readConfig(filepath.Join(benchPath, "sites", site, "site_config.json"))
	if err != nil {
		return "", err
	}
	name, _ := cfg["db_name"].(string)
	return name, nil
}

// QuickSiteCheck decides from files alone, when it can: frappe writes
// site_config.json with a db_name before it creates the database, so a site
// without one never got that far; and on vybench's own MariaDB every database
// is a folder in its data directory, so a site whose folder is missing never
// got its database, even when the server is down.
func QuickSiteCheck(benchPath, site string) (SiteCheck, bool) {
	name, err := siteDBName(benchPath, site)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return SiteCheck{State: SiteIncomplete, Reason: "it has no site_config.json", DBMissing: true}, true
	case err != nil:
		return SiteCheck{State: SiteUnknown, Reason: err.Error()}, true
	case name == "":
		return SiteCheck{State: SiteIncomplete, Reason: "its database was never set up", DBMissing: true}, true
	}
	if dir, ok := siteDatabaseDir(benchPath, site, name); ok && !isDir(dir) {
		return SiteCheck{State: SiteIncomplete, Reason: "its database was never created", DBMissing: true}, true
	}
	return SiteCheck{}, false
}

// siteDatabaseDir is where the site's database lives on disk, when the site
// uses vybench's own MariaDB and that server's data directory is readable.
// Sites pointed at another server (another db_socket) are not judged.
func siteDatabaseDir(benchPath, site, dbName string) (string, bool) {
	sock := ""
	for _, p := range []string{
		filepath.Join(benchPath, "sites", site, "site_config.json"),
		commonConfigPath(benchPath),
	} {
		if cfg, err := readConfig(p); err == nil {
			if s, _ := cfg["db_socket"].(string); s != "" {
				sock = s
				break
			}
		}
	}
	var dataDir, ownSock string
	switch DetectPlatform() {
	case PlatformBrew:
		dataDir, ownSock = filepath.Join(brewVarDir(), "mariadb"), brewRunSocket()
	case PlatformSnap:
		common := os.Getenv("SNAP_COMMON")
		dataDir, ownSock = filepath.Join(common, "mariadb"), filepath.Join(common, "run", "mysql.sock")
	default:
		return "", false
	}
	if sock != ownSock || !isDir(filepath.Join(dataDir, "mysql")) {
		return "", false
	}
	return filepath.Join(dataDir, dbName), true
}

// CheckSite asks bench whether frappe is installed on the site. It takes a
// few seconds, so run it off the UI thread.
func CheckSite(benchPath, site string) SiteCheck {
	if c, ok := QuickSiteCheck(benchPath, site); ok {
		return c
	}
	cmd, err := BenchCommand(benchPath, "--site", site, "list-apps", "--format", "json")
	if err != nil {
		return SiteCheck{State: SiteUnknown, Reason: err.Error()}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	out, runErr := runCaptured(ctx, cmd)
	if ctx.Err() != nil {
		return SiteCheck{State: SiteUnknown, Reason: "bench did not answer within 90 seconds"}
	}
	return classifySiteCheck(site, out, runErr)
}

var (
	pgDatabaseMissing = regexp.MustCompile(`database "[^"]+" does not exist`)
	pgRelationMissing = regexp.MustCompile(`relation "[^"]+" does not exist`)
)

// classifySiteCheck reads `bench --site <site> list-apps --format json`.
// Only failures that prove an unfinished installation count as incomplete; a
// database that is down or refuses the credentials leaves the verdict
// Unknown, so a working site is never mistaken for a broken one.
func classifySiteCheck(site string, out []byte, runErr error) SiteCheck {
	text := string(out)
	if runErr == nil {
		if apps, ok := parseListApps(text, site); ok {
			for _, a := range apps {
				if a == "frappe" {
					return SiteCheck{State: SiteHealthy, Reason: "frappe is installed", Apps: apps}
				}
			}
			return SiteCheck{State: SiteIncomplete, Reason: "frappe is not installed on it"}
		}
		return SiteCheck{State: SiteUnknown, Reason: "bench list-apps printed nothing readable"}
	}
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(text, "(1049") || strings.Contains(lower, "unknown database") || pgDatabaseMissing.MatchString(text):
		return SiteCheck{State: SiteIncomplete, Reason: "its database was never created", DBMissing: true}
	case strings.Contains(text, "(1146") || (strings.Contains(lower, "table") && strings.Contains(lower, "doesn't exist")) ||
		pgRelationMissing.MatchString(text):
		return SiteCheck{State: SiteIncomplete, Reason: "its database is only partly installed"}
	}
	reason := lastLine(text)
	if reason == "" {
		reason = runErr.Error()
	}
	return SiteCheck{State: SiteUnknown, Reason: reason}
}

// parseListApps finds the JSON object list-apps prints, {"site": ["frappe", …]}.
func parseListApps(text, site string) ([]string, bool) {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(l, "{") {
			continue
		}
		var m map[string][]string
		if json.Unmarshal([]byte(l), &m) != nil {
			continue
		}
		if apps, ok := m[site]; ok {
			return apps, true
		}
		for _, apps := range m {
			return apps, true
		}
		return nil, true
	}
	return nil, false
}

func lastLine(text string) string {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(sanitize(lines[i])); l != "" {
			if len(l) > 160 {
				l = l[:157] + "…"
			}
			return l
		}
	}
	return ""
}

// runCaptured runs cmd without a terminal and returns its combined output.
// When ctx ends, the whole process group is terminated.
func runCaptured(ctx context.Context, cmd *exec.Cmd) ([]byte, error) {
	var buf bytes.Buffer
	cmd.Stdin = nil
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	cmd.WaitDelay = 2 * time.Second
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return buf.Bytes(), err
	case <-ctx.Done():
		terminate(cmd)
		err := <-done
		if err == nil {
			err = ctx.Err()
		}
		return buf.Bytes(), err
	}
}

// Describe is the sentence the UI shows for a check of site.
func (c SiteCheck) Describe(site string) string {
	switch c.State {
	case SiteHealthy:
		return fmt.Sprintf("%s already exists and works", site)
	case SiteIncomplete:
		return fmt.Sprintf("%s is left over from an installation that did not finish: %s", site, c.Reason)
	}
	return fmt.Sprintf("could not check %s: %s", site, c.Reason)
}
