package core

// FPM client. The fpm registry is a static file tree (see fpm's
// internal/repository): GET /metadata/index.json lists every package and
// GET /metadata/<org>/<app>/package-metadata.json describes one. There is no
// search endpoint; filtering happens here.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
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
	RegistryURL string   // the primary; what `fpm repo add` is pointed at
	Mirrors     []string // alternates, tried when the primary does not answer
	httpClient  *http.Client
}

// NewFPMClient creates a client for VYBENCH_FPM_REGISTRY or the default
// registry. VYBENCH_FPM_REGISTRY may name several, separated by commas: the
// first is the one repositories are configured against, and the rest are
// mirrors, tried in turn whenever the first does not answer.
//
// Mirrors exist because reaching the registry is not uniformly reliable. It is
// behind Cloudflare, and from a DigitalOcean droplet in sgp1 Cloudflare answers
// from Sao Paulo -- a path that, measured, dropped every request for minutes at
// a time. No retry policy rescues a route that is down; a second origin does.
func NewFPMClient() *FPMClient {
	var urls []string
	for _, u := range strings.Split(os.Getenv("VYBENCH_FPM_REGISTRY"), ",") {
		if u = strings.TrimRight(strings.TrimSpace(u), "/"); u != "" {
			urls = append(urls, u)
		}
	}
	if len(urls) == 0 {
		urls = []string{DefaultRegistryURL}
	}
	return &FPMClient{
		RegistryURL: urls[0],
		Mirrors:     urls[1:],
		httpClient:  newRegistryHTTPClient(),
	}
}

// registries is the primary followed by its mirrors.
func (c *FPMClient) registries() []string {
	return append([]string{c.RegistryURL}, c.Mirrors...)
}

// Reaching the registry is not the uniform problem it looks like. It is behind
// Cloudflare's anycast, and from some networks -- a DigitalOcean droplet in
// sgp1 is the case this was written for -- one advertised address completes the
// TCP handshake and then never answers, so the TLS handshake hangs until the
// client gives up. Go's transport treats a successful dial as the end of
// address selection, so it never tries the other address: the whole 15 seconds
// are spent on the dead one, the marketplace drops to its versionless built-in
// catalog, and every app shows as "latest".
//
// So the handshake happens here, per address, each with its own short deadline,
// which turns the remaining addresses into a real fallback.
// The budgets come from measuring the bad path: a request that is going to
// succeed answers in under two seconds, and one that is going to fail hangs
// until something cuts it off. So the win is in cutting it off early and
// trying again, not in waiting longer -- eight short attempts beat one long
// one by a wide margin, and cost nothing when the first succeeds.
const (
	perAddressBudget  = 3 * time.Second // one address's connect and handshake
	registryTimeout   = 6 * time.Second // one request, across every address
	registryAttempts  = 5               // rounds over the registry list
	registryRetryWait = 250 * time.Millisecond
)

func newRegistryHTTPClient() *http.Client {
	d := &net.Dialer{Timeout: perAddressBudget, KeepAlive: 30 * time.Second}
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          8,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   perAddressBudget,
		ExpectContinueTimeout: time.Second,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialEachAddress(ctx, addr, func(ctx context.Context, a string) (net.Conn, error) {
				return d.DialContext(ctx, network, a)
			})
		},
	}
	// A proxy speaks for every host, so address selection is its problem, not
	// ours: only take the TLS handshake over on a direct connection.
	tr.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			host = addr
		}
		return dialEachAddress(ctx, addr, func(ctx context.Context, a string) (net.Conn, error) {
			raw, err := d.DialContext(ctx, network, a)
			if err != nil {
				return nil, err
			}
			conn := tls.Client(raw, &tls.Config{ServerName: host, NextProtos: []string{"http/1.1"}})
			if err := conn.HandshakeContext(ctx); err != nil {
				_ = raw.Close()
				return nil, err
			}
			return conn, nil
		})
	}
	// No client-level timeout: getJSON gives each attempt its own deadline, so
	// a slow first attempt must not eat the retries' budget.
	return &http.Client{Transport: tr}
}

// dialEachAddress runs attempt against every address the host in addr resolves
// to, giving each its own budget, and returns the first connection that comes
// up. Families are interleaved, so a host with no route to IPv6 costs one
// attempt rather than all of them.
func dialEachAddress(ctx context.Context, addr string, attempt func(context.Context, string) (net.Conn, error)) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return attempt(ctx, addr)
	}
	if net.ParseIP(host) != nil {
		return attempt(ctx, addr)
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(ips) == 0 {
		return attempt(ctx, addr) // let the dialer report the resolution failure
	}
	var firstErr error
	for _, ip := range interleaveFamilies(ips) {
		if ctx.Err() != nil {
			break
		}
		actx, cancel := context.WithTimeout(ctx, perAddressBudget)
		conn, err := attempt(actx, net.JoinHostPort(ip.IP.String(), port))
		cancel()
		if err == nil {
			return conn, nil
		}
		if firstErr == nil {
			firstErr = fmt.Errorf("%s: %w", ip.IP, err)
		}
	}
	if firstErr == nil {
		firstErr = fmt.Errorf("no address for %s could be reached", host)
	}
	return nil, firstErr
}

// interleaveFamilies alternates IPv6 and IPv4 addresses, IPv6 first, the order
// Happy Eyeballs prescribes.
func interleaveFamilies(ips []net.IPAddr) []net.IPAddr {
	var v6, v4 []net.IPAddr
	for _, ip := range ips {
		if ip.IP.To4() == nil {
			v6 = append(v6, ip)
		} else {
			v4 = append(v4, ip)
		}
	}
	out := make([]net.IPAddr, 0, len(ips))
	for i := 0; i < len(v6) || i < len(v4); i++ {
		if i < len(v6) {
			out = append(out, v6[i])
		}
		if i < len(v4) {
			out = append(out, v4[i])
		}
	}
	return out
}

// getJSON fetches and decodes one registry document, retrying a request that
// never got an answer. A refusal is not retried: a 404 says the same thing
// however often it is asked.
// getJSON fetches and decodes one registry document. Each round tries every
// configured registry in turn, so a working mirror is reached in seconds rather
// than after the primary has exhausted its retries. A refusal ends it: a 404
// says the same thing however often it is asked.
func (c *FPMClient) getJSON(ctx context.Context, path string, v any) error {
	var urls []string
	for _, base := range c.registries() {
		u, err := url.JoinPath(base, path)
		if err != nil {
			return err
		}
		urls = append(urls, u)
	}
	var lastErr error
	rounds := 0
	for attempt := 0; attempt < registryAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return giveUp(urls[0], lastErr, rounds, ctx.Err())
			case <-time.After(registryRetryWait):
			}
		}
		for _, u := range urls {
			if ctx.Err() != nil {
				return giveUp(urls[0], lastErr, rounds, ctx.Err())
			}
			rounds++
			err := c.getOnce(ctx, u, v)
			if err == nil {
				return nil
			}
			var refused httpStatusError
			if errors.As(err, &refused) && len(urls) == 1 {
				return err
			}
			lastErr = err
		}
	}
	return giveUp(urls[0], lastErr, rounds, nil)
}

func giveUp(u string, lastErr error, attempts int, ctxErr error) error {
	if lastErr == nil {
		lastErr = ctxErr
	}
	if lastErr == nil {
		lastErr = errors.New("no attempt was made")
	}
	return fmt.Errorf("%s: %w (gave up after %d attempts)", u, lastErr, attempts)
}

// httpStatusError is an answer from the registry that is not 200 -- as opposed
// to no answer at all, which is what the retry loop is for.
type httpStatusError struct {
	URL    string
	Status string
}

func (e httpStatusError) Error() string { return e.URL + " returned " + e.Status }

func (c *FPMClient) getOnce(ctx context.Context, u string, v any) error {
	rctx, cancel := context.WithTimeout(ctx, registryTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, u, nil)
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
		return httpStatusError{URL: u, Status: resp.Status}
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
		candidates = append(candidates,
			filepath.Join(os.Getenv("SNAP"), "bin", "fpm-wrapper"),
			filepath.Join(os.Getenv("SNAP_COMMON"), "bin", "fpm"),
			filepath.Join(os.Getenv("SNAP"), "usr", "bin", "fpm"),
		)
	case PlatformBrew:
		candidates = append(candidates,
			DynamicFPMPath(),
			filepath.Join(brewLibexec(), "bin", "fpm"),
		)
	default:
		candidates = append(candidates,
			DynamicFPMPath(),
			"/usr/local/bin/fpm",
		)
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

// fpmConfigHome returns the directory where fpm configuration and repositories reside.
func fpmConfigHome() string {
	if DetectPlatform() == PlatformSnap {
		if common := os.Getenv("SNAP_COMMON"); common != "" {
			return common
		}
		if userCommon := os.Getenv("SNAP_USER_COMMON"); userCommon != "" {
			return userCommon
		}
	}
	home := os.Getenv("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return home
}

// fpmConfigPath is where fpm keeps its repositories. Under strict snap confinement
// $SNAP_COMMON is accessible to all bench processes and daemons.
func fpmConfigPath() string {
	return filepath.Join(fpmConfigHome(), ".fpm", "config.json")
}

func fpmEnv(benchPath string) []string {
	var env []string
	if benchPath != "" {
		env = benchEnv(benchPath)
	} else {
		env = os.Environ()
	}
	if DetectPlatform() == PlatformSnap {
		if home := fpmConfigHome(); home != "" {
			env = setEnv(env, "HOME", home)
		}
	}
	return env
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
	cmd := exec.Command(fpmBin, "repo", "add", name, registryURL)
	cmd.Dir = fpmConfigHome()
	cmd.Env = fpmEnv("")
	return Step{
		Label: "Adding fpm repository " + registryURL,
		Cmd:   cmd,
	}, true
}

func sameURL(a, b string) bool {
	norm := func(s string) string { return strings.ToLower(strings.TrimRight(strings.TrimSpace(s), "/")) }
	return norm(a) == norm(b)
}

// InstallSpec returns the job that installs pkg into the bench and, when site
// is not empty, onto that site (`fpm install --site` runs install-app).
func (c *FPMClient) InstallSpec(pkg FPMPackage, benchPath, site string) (JobSpec, error) {
	// A linked bench points apps/ and env/ at the read-only package. Copy those
	// trees out once, as a job step, so the install can write into them.
	var prep []Step
	if err := BenchAcceptsApps(benchPath); err != nil {
		if !linkedBench(benchPath) {
			return JobSpec{}, err
		}
		prep = append(prep, Step{
			Label: "Copying apps and env out of the read-only package",
			Fn: func(log func(string)) error {
				log("This is about 1.5 GB and happens once for this bench.")
				if err := MaterialiseLinkedTrees(benchPath); err != nil {
					return err
				}
				return BenchAcceptsApps(benchPath)
			},
		})
	}
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
	cmd.Env = fpmEnv(benchPath)
	return JobSpec{Steps: append(append(prep, steps...), Step{Label: label, Cmd: cmd})}, nil
}

// linkedBench reports whether apps/ or env/ is a symlink into the package.
func linkedBench(benchPath string) bool {
	for _, name := range []string{"apps", "env"} {
		fi, err := os.Lstat(filepath.Join(benchPath, name))
		if err == nil && fi.Mode()&os.ModeSymlink != 0 {
			return true
		}
	}
	return false
}

// MaterialiseLinkedTrees replaces symlink trees (apps, env, sites/assets)
// with real directories copied from their targets. A linked bench cannot
// take an app install until this has run.
func MaterialiseLinkedTrees(benchPath string) error {
	for _, rel := range []string{"apps", "env", filepath.Join("sites", "assets")} {
		if err := replaceSymlinkWithCopy(filepath.Join(benchPath, rel)); err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
	}
	return nil
}

func replaceSymlinkWithCopy(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		return nil
	}
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	tmp := path + ".vybench-copy"
	_ = os.RemoveAll(tmp)
	if err := exec.Command("cp", "-a", target, tmp).Run(); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	if err := os.Remove(path); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	// The package tree is often not writable. The copy belongs to the account
	// that made it, so that account must be able to create files in it.
	return os.Chmod(path, 0o755)
}

// BenchAcceptsApps reports whether an app can be installed into benchPath, and
// explains what to do when it cannot.
func BenchAcceptsApps(benchPath string) error {
	var readonly []string
	for _, t := range []string{"apps", "env"} {
		p := filepath.Join(benchPath, t)
		if !isDir(p) {
			continue
		}
		probe := filepath.Join(p, ".vybench-write-probe")
		f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			readonly = append(readonly, t+"/")
			continue
		}
		_ = f.Close()
		_ = os.Remove(probe)
	}
	if len(readonly) == 0 {
		return nil
	}
	return fmt.Errorf("this bench cannot have apps installed into it: %s are read-only, because they are symlinks "+
		"into the bench shipped with %s. Installing an app writes to both. To make this bench writable (about 1.5 GB, once), "+
		"make it the active bench and run 'sudo snap set %s mode=developer', then 'sudo snap set %s mode=production' to bring "+
		"the services back",
		strings.Join(readonly, " and "), InstanceName(), InstanceName(), InstanceName())
}
