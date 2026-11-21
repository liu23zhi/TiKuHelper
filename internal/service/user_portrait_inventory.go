package service

import (
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

type PortraitInsight struct {
	Key   string                `json:"key"`
	Title string                `json:"title"`
	Total int64                 `json:"total"`
	Items []PortraitSourceSlice `json:"items"`
}

func fillPortraitInventory(db *gorm.DB, scope PortraitScope, portrait *UserPortrait) error {
	portrait.Insights = make([]PortraitInsight, 0, 5)
	for _, spec := range []struct {
		key, title, table, column string
		labels                    map[string]string
	}{
		{"devices", "设备绑定状态", entity.TableNameAccountDeviceBinding, "CASE WHEN revoked_at IS NULL THEN 'active' ELSE 'revoked' END", map[string]string{"active": "使用中", "revoked": "已移除"}},
		{"tokens", "访问令牌状态", entity.TableNameAccessToken, "status", map[string]string{"enabled": "启用", "disabled": "停用"}},
		{"recharges", "充值订单状态", entity.TableNameRechargeOrder, "status", map[string]string{"paid": "已支付", "pending": "未支付", "closed": "已关闭", "refunded": "已退款"}},
		{"refunds", "退款办理状态", "ticket_refunds", "status", map[string]string{"pending_review": "待审核", "frozen": "已审核", "processing": "处理中", "succeeded": "已退款", "rejected": "已驳回", "failed": "失败", "partially_succeeded": "部分退款成功"}},
		{"scripts", "脚本计费模式", entity.TableNameUserScript, "billing_mode", map[string]string{"escrow": "冻结金模式", "activation_code": "一次性激活码", "": "历史模式"}},
	} {
		if !db.Migrator().HasTable(spec.table) {
			continue
		}
		query := db.Session(&gorm.Session{NewDB: true}).Table(spec.table)
		if scope.UserID > 0 {
			query = query.Where("user_id = ?", scope.UserID)
		}
		if spec.key == "scripts" {
			query = query.Where("deleted_at IS NULL")
		}
		var rows []PortraitSourceSlice
		if err := query.Select(spec.column + " AS source, COUNT(*) AS count").Group(spec.column).Order("count DESC").Scan(&rows).Error; err != nil {
			return err
		}
		insight := PortraitInsight{Key: spec.key, Title: spec.title, Items: make([]PortraitSourceSlice, 0, len(rows))}
		for _, row := range rows {
			row.Label = spec.labels[row.Source]
			if row.Label == "" {
				row.Label = row.Source
			}
			if row.Label == "" {
				row.Label = "未记录"
			}
			insight.Total += row.Count
			insight.Items = append(insight.Items, row)
		}
		portrait.Insights = append(portrait.Insights, insight)
	}
	return nil
}
