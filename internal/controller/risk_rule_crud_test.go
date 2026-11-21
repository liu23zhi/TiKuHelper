package controller

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
)

func TestRiskRuleCRUDControllersRequireAdmin(t *testing.T) {
	for name, handler := range map[string]gin.HandlerFunc{
		"types": RiskRuleTypeList, "list": RiskRuleStateList, "create": CreateRiskRule,
		"update": UpdateRiskRule, "delete": DeleteRiskRule,
	} {
		t.Run(name+"/user", func(t *testing.T) {
			resp := performRiskRequest(t, &entity.User{ID: 2, Role: "user"}, http.MethodGet, "/risk/rules", "", nil, handler)
			if resp.Code != http.StatusForbidden {
				t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
			}
		})
	}
}

func TestRiskRuleCRUDControllerRoundTrip(t *testing.T) {
	db := newRiskControllerTestDB(t)
	withRiskControllerDB(t, db)
	admin := &entity.User{ID: 901, Role: "admin"}
	created := performRiskRequest(t, admin, http.MethodPost, "/risk/rules", `{"name":"自定义欠款","description":"用于接口测试","type":"debt_user","config":{},"severity":"high","enabled":false,"threshold":50,"throttleSeconds":3600}`, nil, CreateRiskRule)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	body := readRiskBody(t, created)
	var response struct {
		Data struct {
			RuleID    string `json:"ruleId"`
			Enabled   bool   `json:"enabled"`
			Source    string `json:"source"`
			UpdatedBy int32  `json:"updatedBy"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &response); err != nil {
		t.Fatal(err)
	}
	id := response.Data.RuleID
	if !strings.HasPrefix(id, "custom_") || response.Data.Enabled || response.Data.Source != "custom" || response.Data.UpdatedBy != admin.ID {
		t.Fatalf("unexpected create response: %s", body)
	}
	params := gin.Params{{Key: "ruleId", Value: id}}
	updated := performRiskRequest(t, admin, http.MethodPut, "/risk/rules/"+id, `{"name":"更新名称","description":"更新说明","type":"debt_user","config":{},"severity":"medium","threshold":75,"throttleSeconds":600}`, params, UpdateRiskRule)
	if updated.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", updated.Code, updated.Body.String())
	}
	if strings.Contains(updated.Body.String(), `"enabled":true`) {
		t.Fatalf("metadata edit unexpectedly enabled a disabled rule: %s", updated.Body.String())
	}
	deleted := performRiskRequest(t, admin, http.MethodDelete, "/risk/rules/"+id, "", params, DeleteRiskRule)
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	var row entity.RiskRule
	if err := db.Unscoped().Where("rule_id = ?", id).First(&row).Error; err != nil {
		t.Fatalf("read soft-deleted row: %v", err)
	}
	if row.DeletedAt == nil || row.UpdatedBy != admin.ID || row.Enabled {
		t.Fatalf("delete audit fields not persisted: %+v", row)
	}
	var audits int64
	if err := db.Model(&entity.RiskRuleAudit{}).Where("rule_id = ?", id).Count(&audits).Error; err != nil {
		t.Fatal(err)
	}
	if audits != 3 {
		t.Fatalf("audit rows=%d, want create/update/delete", audits)
	}
}
