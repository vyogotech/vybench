package core

import (
	"slices"
	"testing"
)

func TestValidateSiteName(t *testing.T) {
	for _, ok := range []string{"mysite.localhost", "a", "erp-1.example.com"} {
		if err := ValidateSiteName(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "My.Site", "a..b", "-a.localhost", "a/b", "../x", "a b"} {
		if ValidateSiteName(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestNewSiteSpec(t *testing.T) {
	fakeCLI(t)
	bench := t.TempDir()
	writeFile(t, bench+"/sites/taken.localhost/site_config.json", "{}")

	spec, err := NewSiteSpec(bench, NewSiteOptions{Name: " a.localhost ", AdminPassword: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	args := spec.Steps[0].Cmd.Args
	i := slices.Index(args, "new-site")
	if i < 0 || !slices.Equal(args[i:], []string{"new-site", "a.localhost", "--admin-password", "pw", "--db-type", "mariadb"}) {
		t.Errorf("mariadb args = %v", args)
	}

	spec, err = NewSiteSpec(bench, NewSiteOptions{Name: "pg.localhost", AdminPassword: "pw", DBEngine: "postgres", DBRootPassword: "root"})
	if err != nil {
		t.Fatal(err)
	}
	args = spec.Steps[0].Cmd.Args
	if !slices.Contains(args, "--db-root-username") || args[slices.Index(args, "--db-root-username")+1] != "postgres" ||
		args[len(args)-1] != "root" {
		t.Errorf("postgres args = %v", args)
	}

	if _, err := NewSiteSpec(bench, NewSiteOptions{Name: "b.localhost"}); err == nil {
		t.Error("empty admin password accepted")
	}
	if _, err := NewSiteSpec(bench, NewSiteOptions{Name: "taken.localhost", AdminPassword: "pw"}); err == nil {
		t.Error("existing site accepted")
	}
}

func TestAppHelpers(t *testing.T) {
	fakeCLI(t)
	bench := t.TempDir()
	writeFile(t, bench+"/sites/apps.txt", "frappe\nhrms\nerpnext\n")
	if got := BenchAppNames(bench); !slices.Equal(got, []string{"erpnext", "hrms"}) {
		t.Errorf("BenchAppNames = %v", got)
	}
	spec, _ := NewSiteSpec(bench, NewSiteOptions{Name: "a.localhost", AdminPassword: "pw", InstallApps: []string{"erpnext"}})
	if a := spec.Steps[0].Cmd.Args; !slices.Contains(a, "--install-app") || a[len(a)-1] != "erpnext" {
		t.Errorf("args %v", a)
	}
	spec, _ = InstallAppSpec(bench, "a.localhost", "hrms")
	a := spec.Steps[0].Cmd.Args
	if i := slices.Index(a, "--site"); i < 0 || !slices.Equal(a[i:], []string{"--site", "a.localhost", "install-app", "hrms"}) {
		t.Errorf("install-app args %v", a)
	}
}
