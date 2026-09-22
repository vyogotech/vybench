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
		!slices.Contains(args, "--db-root-password") || args[slices.Index(args, "--db-root-password")+1] != "root" {
		t.Errorf("postgres args = %v", args)
	}
	// bench new-site's --db-socket falls back to the MYSQL_UNIX_PORT envvar
	// when omitted, and every wrapper exports that for its own MariaDB
	// socket. Without an explicit override, a postgres site would inherit
	// the mariadb socket path and libpq would try to dial
	// "<mariadb-socket>/.s.PGSQL.<port>". See CHANGELOG / commit history.
	if i := slices.Index(args, "--db-socket"); i < 0 || args[i+1] != "" {
		t.Errorf("postgres new-site must defeat the MYSQL_UNIX_PORT envvar fallback with an explicit empty --db-socket, got %v", args)
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

func TestMariaDBRootPasswordResolution(t *testing.T) {
	fakeCLI(t)
	bench := t.TempDir()
	writeFile(t, bench+"/config/mariadb_root_password", "secret_mariadb_pw")
	writeFile(t, bench+"/sites/common_site_config.json", `{"db_socket": "/run/test.sock"}`)

	spec, err := NewSiteSpec(bench, NewSiteOptions{Name: "mariadb.localhost", AdminPassword: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	args := spec.Steps[0].Cmd.Args
	if !slices.Contains(args, "--mariadb-root-password") || args[slices.Index(args, "--mariadb-root-password")+1] != "secret_mariadb_pw" {
		t.Errorf("expected --mariadb-root-password secret_mariadb_pw, got %v", args)
	}
	if !slices.Contains(args, "--db-socket") || args[slices.Index(args, "--db-socket")+1] != "/run/test.sock" {
		t.Errorf("expected --db-socket /run/test.sock, got %v", args)
	}

	dropSpec, err := DropSiteSpec(bench, "mariadb.localhost", false, SiteCheck{})
	if err != nil {
		t.Fatal(err)
	}
	dropArgs := dropSpec.Steps[0].Cmd.Args
	if !slices.Contains(dropArgs, "--mariadb-root-password") || dropArgs[slices.Index(dropArgs, "--mariadb-root-password")+1] != "secret_mariadb_pw" {
		t.Errorf("expected --mariadb-root-password in drop-site, got %v", dropArgs)
	}
}

func TestValidateDomainName(t *testing.T) {
	for _, ok := range []string{"example.com", "my-erp.example.co.uk", "sub.domain.localhost"} {
		if err := ValidateDomainName(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"", " ", "a b", "a/b", "A.com", "-a.com", "a..com", "a\\b"} {
		if ValidateDomainName(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestSiteDomains(t *testing.T) {
	bench := t.TempDir()
	
	// Site with no domains
	writeFile(t, bench+"/sites/empty.localhost/site_config.json", `{"db_name": "db1"}`)
	if got := SiteDomains(bench, "empty.localhost"); len(got) != 0 {
		t.Errorf("expected no domains, got %v", got)
	}
	
	// Site with string domains and object domains
	writeFile(t, bench+"/sites/dom.localhost/site_config.json", `{
		"domains": [
			"b.example.com",
			{"domain": "a.example.com", "ssl_certificate": "cert"}
		]
	}`)
	got := SiteDomains(bench, "dom.localhost")
	if !slices.Equal(got, []string{"a.example.com", "b.example.com"}) {
		t.Errorf("expected sorted domains, got %v", got)
	}

	// Missing site_config.json
	if got := SiteDomains(bench, "missing.localhost"); len(got) != 0 {
		t.Errorf("expected no domains for missing config, got %v", got)
	}
}

func TestAddDomainSpec(t *testing.T) {
	fakeCLI(t)
	bench := t.TempDir()

	spec, err := AddDomainSpec(bench, AddDomainOptions{Site: "a.localhost", Domain: "b.com"})
	if err != nil {
		t.Fatal(err)
	}
	args := spec.Steps[0].Cmd.Args
	if !slices.Equal(args[len(args)-5:], []string{"setup", "add-domain", "b.com", "--site", "a.localhost"}) {
		t.Errorf("basic args: %v", args)
	}

	spec, err = AddDomainSpec(bench, AddDomainOptions{Site: "a.localhost", Domain: "b.com", SSLCertificate: "/cert", SSLCertificateKey: "/key"})
	if err != nil {
		t.Fatal(err)
	}
	args = spec.Steps[0].Cmd.Args
	if !slices.Contains(args, "--ssl-certificate") || args[slices.Index(args, "--ssl-certificate")+1] != "/cert" {
		t.Errorf("ssl cert missing: %v", args)
	}
	if !slices.Contains(args, "--ssl-certificate-key") || args[slices.Index(args, "--ssl-certificate-key")+1] != "/key" {
		t.Errorf("ssl key missing: %v", args)
	}

	if _, err := AddDomainSpec(bench, AddDomainOptions{Site: "a.localhost", Domain: ""}); err == nil {
		t.Error("expected error for empty domain")
	}

	if _, err := AddDomainSpec(bench, AddDomainOptions{Site: "", Domain: "b.com"}); err == nil {
		t.Error("expected error for invalid site")
	}
}

// bench restore rejects --db-socket (new-site accepts it), and it once shipped
// anyway: every restore on the snap died with "No such option '--db-socket'"
// after its safety backup had run. Pinned against a bench whose socket resolves,
// because that is the only situation in which the flag was ever added.
func TestRestoreSpecNeverPassesDBSocket(t *testing.T) {
	fakeCLI(t)
	bench := t.TempDir()
	writeFile(t, bench+"/sites/common_site_config.json", `{"db_socket": "/run/test.sock"}`)
	writeFile(t, bench+"/config/mariadb_root_password", "pw")
	writeFile(t, bench+"/sites/a.localhost/site_config.json", `{"db_name":"_x"}`)
	sql := bench + "/sites/a.localhost/private/backups/20260101_000000-a_localhost-database.sql.gz"
	writeFile(t, sql, "x")

	spec, err := RestoreSpec(bench, RestoreOptions{Site: "a.localhost", SQLPath: sql})
	if err != nil {
		t.Fatal(err)
	}
	args := spec.Steps[len(spec.Steps)-1].Cmd.Args
	if slices.Contains(args, "--db-socket") {
		t.Fatalf("bench restore has no --db-socket option, but it was passed: %v", args)
	}
	if !slices.Contains(args, "--mariadb-root-password") {
		t.Fatalf("the root password must still be passed: %v", args)
	}
}
