package core

// FPM client. The fpm registry is a static file tree (see fpm's
// internal/repository): GET /metadata/index.json lists every package and
// GET /metadata/<org>/<app>/package-metadata.json describes one. There is no
// search endpoint; filtering happens here.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DefaultRegistryURL is the public fpm registry. VYBENCH_FPM_REGISTRY overrides it.
const DefaultRegistryURL = "https://fpm.vyogo.tech"

// FPMPackage is one package from the registry index.
type FPMPackage struct {
	Org         string
	Name        string
	Title       string
	Description string
	Version     string // latest version; "" lets fpm pick the newest
	UpdatedAt   string
	Category    string // derived locally; the registry has no categories
}

// FullName returns "org/name".
func (p FPMPackage) FullName() string { return p.Org + "/" + p.Name }

// VersionedName returns the argument for `fpm install`: "org/name==version",
// or "org/name" when no version is known.
func (p FPMPackage) VersionedName() string {
	if p.Version == "" {
		return p.FullName()
	}
	return p.FullName() + "==" + p.Version
}

// DisplayVersion is the version for display.
func (p FPMPackage) DisplayVersion() string {
	if p.Version == "" {
		return "latest"
	}
	return p.Version
}

// PackageDetails is the metadata of one version, shown in the inspector.
type PackageDetails struct {
	Title         string
	License       string
	Author        string
	SourceURL     string
	ReleaseDate   string
	WheelPlatform string
	WheelPython   string
	Dependencies  []string
	RequiredApps  []string
	FrappeCompat  []string
	Versions      []string // newest first
}

// Catalog is the package list plus where it came from.
type Catalog struct {
	Packages []FPMPackage
	Offline  bool   // Packages is the built-in list because the registry failed
	Err      error  // why the registry could not be read
	Source   string // registry URL
}

// FPMClient reads the fpm registry.
type FPMClient struct {
	RegistryURL string
	httpClient  *http.Client
}

// NewFPMClient creates a client for VYBENCH_FPM_REGISTRY or the default registry.
func NewFPMClient() *FPMClient {
	u := os.Getenv("VYBENCH_FPM_REGISTRY")
	if u == "" {
		u = DefaultRegistryURL
	}
	return &FPMClient{
		RegistryURL: strings.TrimRight(u, "/"),
		httpClient:  &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *FPMClient) getJSON(ctx context.Context, path string, v any) error {
	u, err := url.JoinPath(c.RegistryURL, path)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "vybench-tui")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned %s", u, resp.Status)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(v); err != nil {
		return fmt.Errorf("%s did not return a package index: %w", u, err)
	}
	return nil
}

// FetchCatalog loads the registry index, falling back to the built-in list
// (marked Offline) when the registry cannot be read.
func (c *FPMClient) FetchCatalog(ctx context.Context) Catalog {
	var idx struct {
		Packages []struct {
			Org           string `json:"org"`
			AppName       string `json:"appName"`
			Title         string `json:"title"`
			Description   string `json:"description"`
			LatestVersion string `json:"latest_version"`
			UpdatedAt     string `json:"updated_at"`
		} `json:"packages"`
	}
	err := c.getJSON(ctx, "metadata/index.json", &idx)
	if err == nil && len(idx.Packages) == 0 {
		err = errors.New("the registry index lists no packages")
	}
	if err != nil {
		return Catalog{Packages: builtinCatalog(), Offline: true, Err: err, Source: c.RegistryURL}
	}
	var pkgs []FPMPackage
	for _, e := range idx.Packages {
		if e.Org == "" || e.AppName == "" {
			continue
		}
		pkgs = append(pkgs, FPMPackage{
			Org:         e.Org,
			Name:        e.AppName,
			Title:       e.Title,
			Description: strings.TrimSpace(e.Description),
			Version:     e.LatestVersion,
			UpdatedAt:   e.UpdatedAt,
			Category:    CategoryFor(e.AppName),
		})
	}
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].FullName() < pkgs[j].FullName() })
	return Catalog{Packages: pkgs, Source: c.RegistryURL}
}

// FetchDetails loads the metadata of pkg's version (or the latest version).
func (c *FPMClient) FetchDetails(ctx context.Context, pkg FPMPackage) (PackageDetails, error) {
	type dep struct {
		Org               string `json:"org"`
		AppName           string `json:"appName"`
		VersionConstraint string `json:"version_constraint"`
		Version           string `json:"version"`
		VersionSpec       string `json:"version_spec"`
	}
	var meta struct {
		Title         string `json:"title"`
		LatestVersion string `json:"latest_version"`
		Versions      map[string]struct {
			ReleaseDate         string   `json:"release_date"`
			Dependencies        []dep    `json:"dependencies"`
			RequiredApps        []dep    `json:"required_apps"`
			FrappeCompatibility []string `json:"frappe_compatibility"`
			SourceControlURL    string   `json:"source_control_url"`
			Author              string   `json:"author"`
			License             string   `json:"license"`
			WheelPlatform       string   `json:"wheel_platform"`
			WheelPythonVersion  string   `json:"wheel_python_version"`
		} `json:"versions"`
	}
	if err := c.getJSON(ctx, "metadata/"+pkg.Org+"/"+pkg.Name+"/package-metadata.json", &meta); err != nil {
		return PackageDetails{}, err
	}
	d := PackageDetails{Title: meta.Title}
	for v := range meta.Versions {
		d.Versions = append(d.Versions, v)
	}
	sort.Slice(d.Versions, func(i, j int) bool { return CompareVersions(d.Versions[i], d.Versions[j]) > 0 })

	version := pkg.Version
	if version == "" {
		version = meta.LatestVersion
	}
	v, ok := meta.Versions[version]
	if !ok {
		return d, nil
	}
	d.License, d.Author, d.SourceURL = v.License, v.Author, v.SourceControlURL
	d.ReleaseDate, d.WheelPlatform, d.WheelPython = v.ReleaseDate, v.WheelPlatform, v.WheelPythonVersion
	d.FrappeCompat = v.FrappeCompatibility
	depName := func(x dep, constraint string) string {
		name := x.AppName
		if x.Org != "" {
			name = x.Org + "/" + name
		}
		return strings.TrimSpace(name + " " + constraint)
	}
	for _, x := range v.Dependencies {
		d.Dependencies = append(d.Dependencies, depName(x, x.VersionConstraint))
	}
	for _, x := range v.RequiredApps {
		c := x.VersionSpec
		if c == "" {
			c = x.Version
		}
		d.RequiredApps = append(d.RequiredApps, depName(x, c))
	}
	return d, nil
}

// Filter returns packages matching query (case-insensitive substring of the
// name, title, description or org) in category ("" or "all" for any).
func Filter(pkgs []FPMPackage, query, category string) []FPMPackage {
	q := strings.ToLower(strings.TrimSpace(query))
	var out []FPMPackage
	for _, p := range pkgs {
		if category != "" && !strings.EqualFold(category, "all") && !strings.EqualFold(p.Category, category) {
			continue
		}
		if q != "" {
			haystack := strings.ToLower(p.FullName() + " " + p.Title + " " + p.Description)
			if !strings.Contains(haystack, q) {
				continue
			}
		}
		out = append(out, p)
	}
	return out
}

// Categories returns "all" followed by the sorted categories present in pkgs.
func Categories(pkgs []FPMPackage) []string {
	seen := map[string]bool{}
	var cats []string
	for _, p := range pkgs {
		if p.Category != "" && !seen[p.Category] {
			seen[p.Category] = true
			cats = append(cats, p.Category)
		}
	}
	sort.Slice(cats, func(i, j int) bool {
		if (cats[i] == "Other") != (cats[j] == "Other") {
			return cats[j] == "Other" // "Other" last
		}
		return cats[i] < cats[j]
	})
	return append([]string{"all"}, cats...)
}

var knownCategories = map[string]string{
	"erpnext":          "ERP",
	"hrms":             "HR",
	"crm":              "CRM",
	"helpdesk":         "Support",
	"insights":         "BI",
	"builder":          "Web",
	"webshop":          "Web",
	"wiki":             "Docs",
	"lms":              "Education",
	"btu":              "Education",
	"gameplan":         "Collaboration",
	"raven":            "Collaboration",
	"drive":            "Files",
	"print_designer":   "Tools",
	"payments":         "Finance",
	"banking":          "Finance",
	"pos_awesome":      "Retail",
	"marley":           "Healthcare",
	"cargo_management": "Logistics",
}

// CategoryFor maps well-known app names to a category, "Other" otherwise.
func CategoryFor(app string) string {
	if c, ok := knownCategories[app]; ok {
		return c
	}
	return "Other"
}

// builtinCatalog is shown when the registry is unreachable. It carries no
// versions, so an install from it lets fpm resolve the newest one.
func builtinCatalog() []FPMPackage {
	entries := []struct{ name, desc string }{
		{"builder", "An easier way to build web pages for your needs!"},
		{"crm", "Kick-ass Open Source CRM"},
		{"drive", "An easy to use, document sharing and management solution."},
		{"erpnext", "ERP made simple"},
		{"gameplan", "Team discussion and collaboration tool"},
		{"hrms", "Modern HR and Payroll Software"},
		{"insights", "Powerful Reporting Tool for Frappe Apps"},
		{"lms", "Frappe LMS App"},
		{"payments", "Payments app for frappe"},
		{"print_designer", "Frappe App to Design Print Formats using interactive UI."},
		{"webshop", "Open Source eCommerce Platform"},
		{"wiki", "Simple Wiki App"},
	}
	pkgs := make([]FPMPackage, 0, len(entries))
	for _, e := range entries {
		pkgs = append(pkgs, FPMPackage{Org: "frappe", Name: e.name, Description: e.desc, Category: CategoryFor(e.name)})
	}
	return pkgs
}

// ──────────────────────────────────────────────────────────────────────────────
// Installed apps
// ──────────────────────────────────────────────────────────────────────────────

// InstalledApps maps each app in the bench (sites/apps.txt, else apps/) to
// its version, "" when unknown.
func InstalledApps(benchPath string) map[string]string {
	var names []string
	if f, err := os.Open(filepath.Join(benchPath, "sites", "apps.txt")); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if n := strings.TrimSpace(sc.Text()); n != "" {
				names = append(names, n)
			}
		}
		_ = f.Close()
	}
	if len(names) == 0 {
		entries, _ := os.ReadDir(filepath.Join(benchPath, "apps"))
		for _, e := range entries {
			if !strings.HasPrefix(e.Name(), ".") {
				names = append(names, e.Name())
			}
		}
	}
	apps := make(map[string]string, len(names))
	for _, n := range names {
		apps[n] = AppVersion(benchPath, n)
	}
	return apps
}

// CompareVersions orders dotted versions numerically; at an equal core, a
// release sorts above a prerelease ("1.2.0" > "1.2.0-git.…").
func CompareVersions(a, b string) int {
	coreA, preA, _ := strings.Cut(strings.TrimPrefix(a, "v"), "-")
	coreB, preB, _ := strings.Cut(strings.TrimPrefix(b, "v"), "-")
	pa, pb := strings.Split(coreA, "."), strings.Split(coreB, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		x, y := versionPart(pa, i), versionPart(pb, i)
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	switch {
	case preA == preB:
		return 0
	case preA == "":
		return 1
	case preB == "":
		return -1
	}
	return strings.Compare(preA, preB)
}

func versionPart(parts []string, i int) int {
	if i >= len(parts) {
		return 0
	}
	n, _ := strconv.Atoi(parts[i])
	return n
}

// WheelsMatchHost reports whether a package's vendored wheels (its
// wheel_platform tag) fit this machine. It mirrors fpm's own check, which
// refuses a mismatched install unless --ignore-platform-mismatch is given.
func WheelsMatchHost(tag string) bool {
	if tag == "" || tag == "host" {
		return true
	}
	arches := []string{runtime.GOARCH}
	switch runtime.GOARCH {
	case "amd64":
		arches = append(arches, "x86_64")
	case "arm64":
		arches = append(arches, "aarch64")
	}
	for _, p := range strings.Split(tag, ",") {
		p = strings.ToLower(strings.TrimSpace(p))
		archOK := false
		for _, a := range arches {
			archOK = archOK || strings.Contains(p, a)
		}
		osOK := strings.Contains(p, runtime.GOOS)
		if runtime.GOOS == "darwin" {
			osOK = strings.Contains(p, "macosx") || strings.Contains(p, "darwin")
		}
		if p != "" && archOK && osOK {
			return true
		}
	}
	return false
}

// ──────────────────────────────────────────────────────────────────────────────
// Installation
// ──────────────────────────────────────────────────────────────────────────────

// FindFPM locates vyogotech's fpm, never the unrelated Ruby packaging tool of
// the same name that may be first on PATH.
func FindFPM() (string, error) {
	var candidates []string
	if p := os.Getenv("VYBENCH_FPM"); p != "" {
		candidates = append(candidates, p)
	}
	switch DetectPlatform() {
	case PlatformSnap:
		candidates = append(candidates, filepath.Join(os.Getenv("SNAP"), "bin", "fpm-wrapper"))
	case PlatformBrew:
		candidates = append(candidates, filepath.Join(brewLibexec(), "bin", "fpm"))
	}
	for _, c := range candidates {
		if isExecutable(c) {
			return c, nil
		}
	}
	if p, err := exec.LookPath("fpm"); err == nil && isVyogoFPM(p) {
		return p, nil
	}
	return "", errors.New("fpm (the Frappe Package Manager) was not found — reinstall vybench, or set VYBENCH_FPM to an fpm binary from https://github.com/vyogotech/fpm")
}

func isVyogoFPM(path string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, path, "--help").CombinedOutput()
	return bytes.Contains(out, []byte("Frappe Package Manager"))
}

// fpmConfigPath is where fpm keeps its repositories. The snap's fpm-wrapper
// points HOME at $SNAP_USER_COMMON, so the TUI must look there too.
func fpmConfigPath() string {
	home := os.Getenv("HOME")
	if DetectPlatform() == PlatformSnap {
		home = os.Getenv("SNAP_USER_COMMON")
		if home == "" {
			home = os.Getenv("SNAP_COMMON")
		}
	}
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return filepath.Join(home, ".fpm", "config.json")
}

// repositoryStep returns the `fpm repo add` step, or ok=false when a
// configured repository already points at registryURL. fpm has no default
// repository, so without one every install fails with "not found".
func repositoryStep(fpmBin, registryURL string) (Step, bool) {
	var cfg struct {
		Repositories map[string]struct {
			URL string `json:"url"`
		} `json:"repositories"`
	}
	if data, err := os.ReadFile(fpmConfigPath()); err == nil {
		_ = json.Unmarshal(data, &cfg)
	}
	for _, r := range cfg.Repositories {
		if sameURL(r.URL, registryURL) {
			return Step{}, false
		}
	}
	name := "vyogo"
	for i := 2; ; i++ {
		if _, taken := cfg.Repositories[name]; !taken {
			break
		}
		name = fmt.Sprintf("vybench-%d", i)
	}
	return Step{
		Label: "Adding fpm repository " + registryURL,
		Cmd:   exec.Command(fpmBin, "repo", "add", name, registryURL),
	}, true
}

func sameURL(a, b string) bool {
	norm := func(s string) string { return strings.ToLower(strings.TrimRight(strings.TrimSpace(s), "/")) }
	return norm(a) == norm(b)
}

// InstallSpec returns the job that installs pkg into the bench and, when site
// is not empty, onto that site (`fpm install --site` runs install-app).
func (c *FPMClient) InstallSpec(pkg FPMPackage, benchPath, site string) (JobSpec, error) {
	fpmBin, err := FindFPM()
	if err != nil {
		return JobSpec{}, err
	}
	var steps []Step
	if s, need := repositoryStep(fpmBin, c.RegistryURL); need {
		steps = append(steps, s)
	}
	args := []string{"install", pkg.VersionedName(), "--bench-path", benchPath}
	label := "Installing " + pkg.VersionedName() + " into the bench"
	if site != "" {
		args = append(args, "--site", site)
		label += " and onto " + site
	}
	cmd := exec.Command(fpmBin, args...)
	cmd.Dir = benchPath
	cmd.Env = benchEnv(benchPath)
	return JobSpec{Steps: append(steps, Step{Label: label, Cmd: cmd})}, nil
}
