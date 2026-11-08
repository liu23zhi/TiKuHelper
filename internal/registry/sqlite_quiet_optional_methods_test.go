package registry

import (
	"errors"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"testing"
)

type integrationNoSavepointDriver struct{ gorm.Dialector }

func TestIntegrationQuietUnsupportedSavepointIsError(t *testing.T) {
	// This test is candidate-only because calling the new optional methods is a
	// new API. It is excluded by the shared B/M/R -run expression.
	d := sqliteQuietDialector{Dialector: integrationNoSavepointDriver{sqlite.Open("file:unused?mode=memory&cache=shared")}}
	e := d.SavePoint(nil, "synthetic")
	if !errors.Is(e, gorm.ErrUnsupportedDriver) {
		t.Fatalf("unsupported driver silently succeeded: %v", e)
	}
	if e = d.RollbackTo(nil, "synthetic"); !errors.Is(e, gorm.ErrUnsupportedDriver) {
		t.Fatalf("unsupported rollback silently succeeded: %v", e)
	}
}
