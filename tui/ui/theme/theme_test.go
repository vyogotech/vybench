package theme

import (
	"strings"
	"testing"
)

func TestThemeBadges(t *testing.T) {
	pg := DBBadge("postgres")
	if !strings.Contains(pg, "Postgres") {
		t.Errorf("expected Postgres in badge, got %s", pg)
	}

	maria := DBBadge("mariadb")
	if !strings.Contains(maria, "MariaDB") {
		t.Errorf("expected MariaDB in badge, got %s", maria)
	}

	other := DBBadge("other")
	if !strings.Contains(other, "MariaDB") {
		t.Errorf("expected MariaDB in default badge, got %s", other)
	}

	active := ActiveBadge(true)
	if !strings.Contains(active, "active") {
		t.Errorf("expected active in badge, got %s", active)
	}

	inactive := ActiveBadge(false)
	if !strings.Contains(inactive, "──") {
		t.Errorf("expected ── in inactive badge, got %s", inactive)
	}
}
