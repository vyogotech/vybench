package core

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

// The recurring bug class: the TUI builds a `bench <subcommand>` line with an
// option that subcommand does not have. bench answers "No such option" and the
// whole action fails -- twice already, and both times only when a human drove
// the TUI on a real install, because every unit test compared the command line
// with itself. `--db-socket` is accepted by new-site but by neither restore nor
// drop-site; it was passed to both.
//
// testdata/bench_flags.json is the ground truth: each subcommand's option list
// captured from `bench <subcommand> --help` on a real snap install. This test
// builds every command the TUI can issue, with every optional branch on, and
// checks each option against ITS OWN subcommand's list -- not a merged one,
// which is precisely how the first audit missed drop-site.
//
// To refresh after a bench upgrade: re-run the capture in that file's _note.

var benchGlobalFlags = []string{"--site", "--help"} // accepted before/at any subcommand

func loadBenchFlags(t *testing.T) map[string][]string {
	t.Helper()
	data, err := os.ReadFile("testdata/bench_flags.json")
	if err != nil {
		t.Fatal(err)
	}
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for k, v := range raw {
		var flags []string
		if json.Unmarshal(v, &flags) == nil {
			out[k] = flags
		}
	}
	return out
}

// checkFlags fails for every --option in args that subcommand does not have.
func checkFlags(t *testing.T, known map[string][]string, subcommand string, args []string) {
	t.Helper()
	allowed, ok := known[subcommand]
	if !ok || len(allowed) == 0 {
		t.Fatalf("no recorded flags for %q: refresh testdata/bench_flags.json", subcommand)
	}
	start := slices.Index(args, strings.Fields(subcommand)[0])
	if start < 0 {
		t.Fatalf("%q not found in %v", subcommand, args)
	}
	for _, a := range args[start:] {
		if !strings.HasPrefix(a, "--") {
			continue
		}
		if slices.Contains(allowed, a) || slices.Contains(benchGlobalFlags, a) {
			continue
		}
		t.Errorf("`bench %s` has no option %s -- it would fail with \"No such option\".\n  command: %v\n  accepted: %v",
			subcommand, a, args, allowed)
	}
}

func TestSpecFlagsExistInRealBench(t *testing.T) {
	fakeCLI(t)
	known := loadBenchFlags(t)

	// A bench whose socket and root password both resolve: the only situation
	// in which the bad --db-socket was ever added.
	bench := t.TempDir()
	writeFile(t, bench+"/sites/common_site_config.json", `{"db_socket": "/run/test.sock"}`)
	writeFile(t, bench+"/config/mariadb_root_password", "pw")
	writeFile(t, bench+"/sites/exists.localhost/site_config.json", `{"db_name":"_x"}`)
	sql := bench + "/sites/exists.localhost/private/backups/20260101_000000-exists_localhost-database.sql.gz"
	writeFile(t, sql, "x")
	pub := bench + "/sites/exists.localhost/private/backups/20260101_000000-exists_localhost-files.tar"
	priv := bench + "/sites/exists.localhost/private/backups/20260101_000000-exists_localhost-private-files.tar"
	writeFile(t, pub, "x")
	writeFile(t, priv, "x")

	last := func(spec JobSpec, err error) []string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return spec.Steps[len(spec.Steps)-1].Cmd.Args
	}

	t.Run("new-site mariadb, every option", func(t *testing.T) {
		checkFlags(t, known, "new-site", last(NewSiteSpec(bench, NewSiteOptions{
			Name: "fresh.localhost", AdminPassword: "a", DBEngine: "mariadb", InstallApps: []string{"erpnext"},
		})))
	})
	t.Run("new-site replacing an existing site", func(t *testing.T) {
		checkFlags(t, known, "new-site", last(NewSiteSpec(bench, NewSiteOptions{
			Name: "exists.localhost", AdminPassword: "a", DBEngine: "mariadb", Force: true,
		})))
	})
	t.Run("new-site postgres", func(t *testing.T) {
		checkFlags(t, known, "new-site", last(NewSiteSpec(bench, NewSiteOptions{
			Name: "pg.localhost", AdminPassword: "a", DBEngine: "postgres", DBRootUser: "postgres", DBRootPassword: "x",
		})))
	})
	t.Run("drop-site", func(t *testing.T) {
		checkFlags(t, known, "drop-site", last(DropSiteSpec(bench, "exists.localhost", false, SiteCheck{})))
	})
	t.Run("drop-site --force", func(t *testing.T) {
		checkFlags(t, known, "drop-site", last(DropSiteSpec(bench, "exists.localhost", true, SiteCheck{})))
	})
	t.Run("restore, every option", func(t *testing.T) {
		checkFlags(t, known, "restore", last(RestoreSpec(bench, RestoreOptions{
			Site: "exists.localhost", SQLPath: sql, PublicFiles: pub, PrivateFiles: priv,
			Force: true, DBRootUser: "root", DBRootPassword: "x",
		})))
	})
	t.Run("restore, managed password", func(t *testing.T) {
		checkFlags(t, known, "restore", last(RestoreSpec(bench, RestoreOptions{Site: "exists.localhost", SQLPath: sql})))
	})
	t.Run("backup", func(t *testing.T) {
		checkFlags(t, known, "backup", last(BackupSpec(bench, "exists.localhost")))
	})
	t.Run("install-app", func(t *testing.T) {
		checkFlags(t, known, "install-app", last(InstallAppSpec(bench, "exists.localhost", "erpnext")))
	})
	t.Run("setup add-domain with SSL", func(t *testing.T) {
		checkFlags(t, known, "setup add-domain", last(AddDomainSpec(bench, AddDomainOptions{
			Site: "exists.localhost", Domain: "shop.example.com", SSLCertificate: "/c.pem", SSLCertificateKey: "/k.pem",
		})))
	})
	t.Run("list-apps", func(t *testing.T) {
		// site_health.go builds this inline as: --site S list-apps --format json
		checkFlags(t, known, "list-apps", []string{"--site", "exists.localhost", "list-apps", "--format", "json"})
	})
}
