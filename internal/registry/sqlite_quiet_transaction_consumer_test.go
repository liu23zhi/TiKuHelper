package registry

import (
	"errors"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"testing"
)

type integrationQuietParent struct {
	ID int `gorm:"primaryKey"`
}

func (integrationQuietParent) TableName() string { return "integration_quiet_parents" }

type integrationQuietChild struct {
	ID       int `gorm:"primaryKey"`
	ParentID int
	Parent   integrationQuietParent `gorm:"foreignKey:ParentID;references:ID;constraint:OnDelete:RESTRICT"`
}

func (integrationQuietChild) TableName() string { return "integration_quiet_children" }
func integrationQuietDB(t *testing.T) *gorm.DB {
	t.Helper()
	d, e := gorm.Open(sqliteQuietDialector{Dialector: sqlite.Open("file:" + t.Name() + "?mode=memory&cache=shared")}, &gorm.Config{})
	if e != nil {
		t.Fatal(e)
	}
	s, e := d.DB()
	if e != nil {
		t.Fatal(e)
	}
	s.SetMaxOpenConns(1)
	t.Cleanup(func() { s.Close() })
	return d
}
func TestIntegrationQuietOptionalSavepointConsumer(t *testing.T) {
	d := integrationQuietDB(t)
	if e := d.AutoMigrate(&integrationQuietParent{}); e != nil {
		t.Fatal(e)
	}
	reject := errors.New("SYNTHETIC_INNER_REJECT")
	e := d.Transaction(func(tx *gorm.DB) error {
		if e := tx.Create(&integrationQuietParent{ID: 1}).Error; e != nil {
			return e
		}
		inner := tx.Transaction(func(n *gorm.DB) error {
			if e := n.Create(&integrationQuietParent{ID: 2}).Error; e != nil {
				return e
			}
			return reject
		})
		if !errors.Is(inner, reject) {
			return inner
		}
		return tx.Create(&integrationQuietParent{ID: 3}).Error
	})
	if e != nil {
		t.Errorf("SQLITE_PORT_EXPECTED_PRODUCT_RED quiet optional SavePoint lost: %v", e)
		return
	}
	var rows []integrationQuietParent
	if e = d.Order("id").Find(&rows).Error; e != nil {
		t.Fatal(e)
	}
	if len(rows) != 2 || rows[0].ID != 1 || rows[1].ID != 3 {
		t.Errorf("caller transaction/savepoint ownership changed: %+v", rows)
	}
}
func TestIntegrationQuietStillCreatesConstraintInsideTransaction(t *testing.T) {
	d := integrationQuietDB(t)
	if e := d.AutoMigrate(&integrationQuietParent{}); e != nil {
		t.Fatal(e)
	}
	if e := d.Exec("CREATE TABLE integration_quiet_children(id INTEGER PRIMARY KEY,parent_id INTEGER)").Error; e != nil {
		t.Fatal(e)
	}
	if e := d.Transaction(func(tx *gorm.DB) error { return tx.Migrator().CreateConstraint(&integrationQuietChild{}, "Parent") }); e != nil {
		t.Errorf("SQLITE_PORT_EXPECTED_PRODUCT_RED quiet disabled a real constraint rebuild: %v", e)
		return
	}
	var rows []struct{ Table string }
	if e := d.Raw("PRAGMA foreign_key_list('integration_quiet_children')").Scan(&rows).Error; e != nil {
		t.Fatal(e)
	}
	if len(rows) != 1 || rows[0].Table != "integration_quiet_parents" {
		t.Errorf("constraint creation skipped: %+v", rows)
	}
}
