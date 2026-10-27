package migrations

import (
	"strings"
	"testing"
)

func TestPublicSourceMigrationIsAdditiveAndReversible(t *testing.T) {
	b, err := Files.ReadFile("000015_public_source_username.sql")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	up := strings.Split(s, "-- +goose Down")[0]
	if !strings.Contains(up, "ADD COLUMN IF NOT EXISTS source_username TEXT") || strings.Contains(up, "NOT NULL") || strings.Contains(up, "UPDATE ") || strings.Contains(up, "DELETE ") {
		t.Fatal("not nullable additive migration")
	}
	if !strings.Contains(s, "DROP COLUMN IF EXISTS source_username") {
		t.Fatal("missing down")
	}
}
