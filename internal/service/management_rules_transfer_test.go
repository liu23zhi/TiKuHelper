package service

import (
	"fmt"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
)

func TestManagementRuleTransfersValidatePreviewAndPreserveDisabled(t *testing.T) {
	for _, resource := range []string{"blacklist", "whitelist"} {
		t.Run(resource, func(t *testing.T) {
			db := managementTransferDB(t)
			if err := db.AutoMigrate(&entity.BlacklistEntry{}, &entity.WhitelistEntry{}); err != nil {
				t.Fatal(err)
			}
			content := []byte(`[{"kind":"ip","value":"203.0.113.9/24","reason":"测试","enabled":false,"expires_at":"2030-01-01T00:00:00Z"}]`)
			preview, err := ImportDataWithDB(db, ImportDataOptions{Resource: resource, Filename: "rules.json", Content: content, DryRun: true})
			if err != nil || preview.Succeeded != 1 {
				t.Fatalf("preview=%+v err=%v", preview, err)
			}
			result, err := ImportDataWithDB(db, ImportDataOptions{Resource: resource, Filename: "rules.json", Content: content})
			if err != nil || result.Succeeded != 1 {
				t.Fatalf("import=%+v err=%v", result, err)
			}
			var stored struct {
				Pattern   string
				Enabled   bool
				ExpiresAt *time.Time
			}
			if err := db.Table(resource + "_entries").First(&stored).Error; err != nil {
				t.Fatal(err)
			}
			if stored.Pattern != "203.0.113.0/24" || stored.Enabled || stored.ExpiresAt == nil || stored.ExpiresAt.Year() != 2030 {
				t.Fatalf("stored=%+v", stored)
			}
			plain := false
			for _, format := range []string{"csv", "json"} {
				output, err := ExportDataWithDB(db, ExportDataOptions{Resource: resource, Format: format, Masked: &plain})
				if err != nil || output.Rows != 1 {
					t.Fatalf("export=%+v err=%v", output, err)
				}
				if err := db.Table(resource + "_entries").Where("1 = 1").Delete(map[string]any{}).Error; err != nil {
					t.Fatal(err)
				}
				reimport, err := ImportDataWithDB(db, ImportDataOptions{Resource: resource, Format: format, Content: output.Content})
				if err != nil || reimport.Succeeded != 1 {
					t.Fatalf("roundtrip %s=%+v err=%v", format, reimport, err)
				}
			}
			invalid, err := ImportDataWithDB(db, ImportDataOptions{Resource: resource, Filename: "bad.json", DryRun: true, Content: []byte(`[{"kind":"ip","value":"bad-ip","reason":"bad"}]`)})
			if err != nil || invalid.Failed != 1 {
				t.Fatalf("invalid preview=%+v err=%v", invalid, err)
			}
		})
	}
}

func TestManagementBlacklistAllKinds(t *testing.T) {
	db := managementTransferDB(t)
	if err := db.AutoMigrate(&entity.BlacklistEntry{}); err != nil {
		t.Fatal(err)
	}
	for kind, value := range map[string]string{"ip": "203.0.113.5", "phone": "13812345678", "wechat": "openid-transfer", "region": "云南 玉溪"} {
		input := []byte(fmt.Sprintf(`[{"kind":%q,"value":%q,"reason":"测试"}]`, kind, value))
		result, err := ImportDataWithDB(db, ImportDataOptions{Resource: "blacklist", Format: "json", Content: input})
		if err != nil || result.Succeeded != 1 {
			t.Fatalf("%s result=%+v err=%v", kind, result, err)
		}
	}
}
