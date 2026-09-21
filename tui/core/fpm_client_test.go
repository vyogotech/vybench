package core

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

const testIndex = `{"packages": [
  {"org": "frappe", "appName": "hrms", "description": "Modern HR", "latest_version": "16.18.1"},
  {"org": "acme", "appName": "widgets", "title": "Widgets", "description": "Custom widgets", "latest_version": "1.0.0"},
  {"org": "frappe", "appName": "crm", "description": "Kick-ass Open Source CRM", "latest_version": "1.83.0"}
]}`

const testCRM = `{
  "org": "frappe", "appName": "crm", "title": "Frappe CRM", "latest_version": "1.83.0",
  "versions": {
    "1.82.0": {"license": "old"},
    "1.83.0": {
      "license": "AGPLv3", "author": "Frappe", "release_date": "2026-09-04T05:59:13Z",
      "source_control_url": "https://github.com/frappe/crm",
      "wheel_platform": "manylinux2014_x86_64", "wheel_python_version": "3.14",
      "frappe_compatibility": ["16"],
      "dependencies": [{"org": "frappe", "appName": "frappe", "version_constraint": ">=16"}],
      "required_apps": [{"appName": "erpnext", "version_spec": ">=16.0.0"}]
    },
    "1.9.0": {}
  }
}`

func registry(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/metadata/index.json":
			fmt.Fprint(w, testIndex)
		case "/metadata/frappe/crm/package-metadata.json":
			fmt.Fprint(w, testCRM)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchCatalog(t *testing.T) {
	c := &FPMClient{RegistryURL: registry(t).URL + "/", httpClient: http.DefaultClient}
	cat := c.FetchCatalog(context.Background())
	if cat.Offline || cat.Err != nil {
		t.Fatalf("offline: %v", cat.Err)
	}
	var names []string
	for _, p := range cat.Packages {
		names = append(names, p.FullName())
	}
	if !slices.Equal(names, []string{"acme/widgets", "frappe/crm", "frappe/hrms"}) {
		t.Errorf("packages = %v", names)
	}
	if cat.Packages[1].Category != "CRM" || cat.Packages[0].Category != "Other" {
		t.Errorf("categories: %+v", cat.Packages)
	}
	if got := Categories(cat.Packages); !slices.Equal(got, []string{"all", "CRM", "HR", "Other"}) {
		t.Errorf("Categories = %v", got)
	}
}

// The old client pointed at a URL that redirected to an HTML dashboard and
// silently showed a hard-coded list. The fallback must be flagged.
func TestFetchCatalogFallsBackVisibly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<!DOCTYPE html><html>dashboard</html>")
	}))
	defer srv.Close()
	cat := (&FPMClient{RegistryURL: srv.URL, httpClient: http.DefaultClient}).FetchCatalog(context.Background())
	if !cat.Offline || cat.Err == nil || len(cat.Packages) == 0 {
		t.Fatalf("fallback not flagged: %+v", cat)
	}
	for _, p := range cat.Packages {
		if p.Version != "" || strings.Contains(p.VersionedName(), "==") {
			t.Errorf("offline entry %s pins a version it cannot know", p.FullName())
		}
	}
}

func TestFetchDetails(t *testing.T) {
	c := &FPMClient{RegistryURL: registry(t).URL, httpClient: http.DefaultClient}
	d, err := c.FetchDetails(context.Background(), FPMPackage{Org: "frappe", Name: "crm", Version: "1.83.0"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Title != "Frappe CRM" || d.License != "AGPLv3" || d.WheelPlatform != "manylinux2014_x86_64" || d.WheelPython != "3.14" {
		t.Errorf("details: %+v", d)
	}
	if !slices.Equal(d.Versions, []string{"1.83.0", "1.82.0", "1.9.0"}) {
		t.Errorf("versions = %v, want newest first", d.Versions)
	}
	if !slices.Equal(d.Dependencies, []string{"frappe/frappe >=16"}) || !slices.Equal(d.RequiredApps, []string{"erpnext >=16.0.0"}) {
		t.Errorf("deps %v required %v", d.Dependencies, d.RequiredApps)
	}
	if _, err := c.FetchDetails(context.Background(), FPMPackage{Org: "x", Name: "missing"}); err == nil {
		t.Error("404 not reported")
	}
}

func TestFilterAndNaming(t *testing.T) {
	pkgs := []FPMPackage{
		{Org: "frappe", Name: "erpnext", Category: "ERP", Description: "Core ERP"},
		{Org: "frappe", Name: "hrms", Category: "HR", Description: "HR and Payroll"},
		{Org: "frappe", Name: "crm", Title: "Frappe CRM", Category: "CRM", Description: "Sales"},
	}
	if got := Filter(pkgs, "", "HR"); len(got) != 1 || got[0].Name != "hrms" {
		t.Errorf("category: %v", got)
	}
	if got := Filter(pkgs, "PAYROLL", "all"); len(got) != 1 {
		t.Errorf("case-insensitive search: %v", got)
	}
	if got := Filter(pkgs, "frappe crm", ""); len(got) != 1 || got[0].Name != "crm" {
		t.Errorf("title search: %v", got)
	}
	if got := Filter(pkgs, "sales", "ERP"); len(got) != 0 {
		t.Errorf("combined: %v", got)
	}
	p := FPMPackage{Org: "frappe", Name: "hrms", Version: "16.18.1"}
	if p.FullName() != "frappe/hrms" || p.VersionedName() != "frappe/hrms==16.18.1" {
		t.Errorf("naming: %s %s", p.FullName(), p.VersionedName())
	}
	if (FPMPackage{Org: "a", Name: "b"}).VersionedName() != "a/b" {
		t.Error("an unknown version must not be pinned")
	}
}

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"1.83.0", "1.82.0", 1}, {"1.9.0", "1.10.0", -1}, {"16.0", "16.0.0", 0},
		{"1.2.0", "1.2.0-git.20260903.abc", 1}, {"v2", "1.99", 1}, {"1.0.0", "1.0.0", 0},
	} {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestWheelsMatchHost(t *testing.T) {
	arch := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[runtime.GOARCH]
	osTag := map[string]string{"linux": "manylinux2014", "darwin": "macosx_11_0"}[runtime.GOOS]
	if arch == "" || osTag == "" {
		t.Skip("no wheel tag mapping for this platform")
	}
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		arch = "arm64"
	}
	if !WheelsMatchHost("host") || !WheelsMatchHost("") {
		t.Error("host tag rejected")
	}
	if !WheelsMatchHost("win_amd64, " + osTag + "_" + arch) {
		t.Error("tag for this machine rejected")
	}
	foreign := "manylinux2014_s390x"
	if WheelsMatchHost(foreign) {
		t.Errorf("%s accepted on %s/%s", foreign, runtime.GOOS, runtime.GOARCH)
	}
}

func TestInstallSpec(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SNAP", "")
	fakeFPM := filepath.Join(t.TempDir(), "fpm")
	writeFile(t, fakeFPM, "#!/bin/sh\necho Frappe Package Manager\n")
	if err := os.Chmod(fakeFPM, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VYBENCH_FPM", fakeFPM)

	c := &FPMClient{RegistryURL: "https://fpm.vyogo.tech"}
	pkg := FPMPackage{Org: "frappe", Name: "crm", Version: "1.83.0"}

	// No fpm config yet: the repository is added first.
	spec, err := c.InstallSpec(pkg, "/benches/b", "site1.localhost")
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Steps) != 2 || !slices.Equal(spec.Steps[0].Cmd.Args[1:], []string{"repo", "add", "vyogo", "https://fpm.vyogo.tech"}) {
		t.Fatalf("steps: %+v", spec.Steps)
	}
	install := spec.Steps[1].Cmd
	if !slices.Equal(install.Args[1:], []string{"install", "frappe/crm==1.83.0", "--bench-path", "/benches/b", "--site", "site1.localhost"}) {
		t.Errorf("install args = %v", install.Args)
	}
	if !slices.Contains(install.Env, "FRAPPE_BENCH_ROOT=/benches/b") {
		t.Error("install does not pin FRAPPE_BENCH_ROOT to the target bench")
	}

	// A repository already pointing at the registry (trailing slash and all).
	writeFile(t, filepath.Join(home, ".fpm", "config.json"), `{"repositories": {"mine": {"url": "https://FPM.vyogo.tech/"}}}`)
	if spec, _ := c.InstallSpec(pkg, "/b", ""); len(spec.Steps) != 1 {
		t.Errorf("repository added again: %d steps", len(spec.Steps))
	}

	// The name "vyogo" taken by another URL.
	writeFile(t, filepath.Join(home, ".fpm", "config.json"), `{"repositories": {"vyogo": {"url": "https://mirror.example"}}}`)
	if spec, _ := c.InstallSpec(pkg, "/b", ""); spec.Steps[0].Cmd.Args[3] != "vybench-2" {
		t.Errorf("repo name = %v", spec.Steps[0].Cmd.Args)
	}
}

func TestFindFPMSkipsRubyFPM(t *testing.T) {
	t.Setenv("VYBENCH_FPM", "")
	t.Setenv("SNAP", "")
	t.Setenv("VYBENCH_LIBEXEC", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "fpm"), "#!/bin/sh\necho 'Effing Package Management'\n")
	if err := os.Chmod(filepath.Join(dir, "fpm"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if p, err := FindFPM(); err == nil {
		t.Errorf("picked the Ruby fpm at %s", p)
	}
}

func TestInstalledApps(t *testing.T) {
	bench := t.TempDir()
	writeFile(t, filepath.Join(bench, "sites", "apps.txt"), "frappe\ncrm\n\n")
	writeFile(t, filepath.Join(bench, "apps", "crm", "crm", "__init__.py"), `__version__ = "1.82.0"`)
	apps := InstalledApps(bench)
	if len(apps) != 2 || apps["crm"] != "1.82.0" || apps["frappe"] != "" {
		t.Errorf("InstalledApps = %v", apps)
	}
}
