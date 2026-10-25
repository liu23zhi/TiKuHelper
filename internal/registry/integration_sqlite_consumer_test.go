package registry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/configs"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	gl "gorm.io/gorm/logger"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const integrationLookupName = "idx_billing_records_user_request_type_id"

func integrationCatalogHash(t *testing.T, d *gorm.DB) string {
	t.Helper()
	var rows []struct{ Type, Name, Table, SQL string }
	if e := d.Raw("SELECT type,name,tbl_name AS `table`,COALESCE(sql,'') AS sql FROM sqlite_master ORDER BY type,name").Scan(&rows).Error; e != nil {
		t.Fatal(e)
	}
	var b bytes.Buffer
	for _, r := range rows {
		b.WriteString(r.Type + "|" + r.Name + "|" + r.Table + "|" + r.SQL + "\n")
	}
	h := sha256.Sum256(b.Bytes())
	return hex.EncodeToString(h[:])
}
func TestIntegrationSQLiteBillingStartupConsumer(t *testing.T) {
	for _, kind := range []string{"missing", "wrong_definition"} {
		t.Run(kind, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "startup.sqlite")
			d, e := gorm.Open(sqlite.Open(p), &gorm.Config{Logger: gl.Default.LogMode(gl.Silent)})
			if e != nil {
				t.Fatal(e)
			}
			if e = d.AutoMigrate(&entity.BillingRecord{}); e != nil {
				t.Fatal(e)
			}
			if e = d.Create(&entity.BillingRecord{ID: 7, UserID: 9, Type: "normal_search", Amount: 0.000062, RequestID: "INTEGRATION_SYNTHETIC"}).Error; e != nil {
				t.Fatal(e)
			}
			if kind == "wrong_definition" {
				if e = d.Exec("CREATE INDEX " + integrationLookupName + " ON billing_records(request_id,user_id,type,id)").Error; e != nil {
					t.Fatal(e)
				}
			}
			before := integrationCatalogHash(t, d)
			raw, _ := d.DB()
			if e = raw.Close(); e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestIntegrationSQLiteBillingConsumerChild$", "-test.count=1", "-test.v")
			cmd.Env = append(os.Environ(), "INTEGRATION_SQLITE_CHILD="+kind, "INTEGRATION_SQLITE_DB="+p, "TIKU_APP_MODE=production", "TIKU_ADMIN_PASSWORD=IntegrationFixtureAdmin123!", "SQL_DSN=")
			var out, errout bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &errout
			e = cmd.Run()
			code := 0
			if e != nil {
				if x, ok := e.(*exec.ExitError); ok {
					code = x.ExitCode()
				} else {
					t.Fatalf("consumer subprocess infrastructure: %T", e)
				}
			}
			if ctx.Err() != nil {
				t.Fatal("consumer subprocess timed out")
			}
			t.Logf("INTEGRATION_CONSUMER_CHILD kind=%s exit=%d stdout=%s stderr=%s", kind, code, out.String(), errout.String())
			after, e := gorm.Open(sqlite.Open(p), &gorm.Config{Logger: gl.Default.LogMode(gl.Silent)})
			if e != nil {
				t.Fatal(e)
			}
			h, _ := after.DB()
			defer h.Close()
			if kind == "missing" {
				if code != 0 || !strings.Contains(out.String(), "INTEGRATION_INDEX_PRESENT") {
					t.Errorf("SQLITE_PORT_EXPECTED_PRODUCT_RED startup omitted the approved billing index")
				}
			}
			if kind == "wrong_definition" {
				if code != 1 || !strings.Contains(out.String()+errout.String(), "billing_index_startup class=shape_mismatch") {
					t.Errorf("SQLITE_PORT_EXPECTED_PRODUCT_RED conflicting index was not rejected by genuine startup")
				}
				if integrationCatalogHash(t, after) != before {
					t.Errorf("SQLITE_PORT_EXPECTED_PRODUCT_RED conflict startup changed preexisting schema")
				}
			}
		})
	}
}
func TestIntegrationSQLiteBillingConsumerChild(t *testing.T) {
	kind := os.Getenv("INTEGRATION_SQLITE_CHILD")
	if kind == "" {
		t.Skip("bounded child helper")
	}
	p := os.Getenv("INTEGRATION_SQLITE_DB")
	if !filepath.IsAbs(p) || filepath.Base(p) != "startup.sqlite" || !strings.HasPrefix(p, os.TempDir()+string(os.PathSeparator)) {
		t.Fatal("own fixture path required")
	}
	resetRegistryDBState(t)
	d, e := RegisterDBWithError(configs.Config{Database: configs.DatabaseConfig{Path: p}})
	if e != nil || d == nil {
		t.Fatal("preflight/startup fixture error")
	}
	if !d.Migrator().HasTable(&entity.IdempotencyRecord{}) {
		t.Fatal("upstream idempotency model omitted")
	}
	if !d.Migrator().HasColumn(&entity.BillingRecord{}, "SearchMode") {
		t.Fatal("upstream search-mode column omitted")
	}
	if d.Migrator().HasIndex(&entity.BillingRecord{}, integrationLookupName) {
		t.Log("INTEGRATION_INDEX_PRESENT")
	} else {
		t.Log("INTEGRATION_INDEX_MISSING")
	}
	if e := CloseDB(); e != nil {
		t.Fatal(e)
	}
}
