package entity

import "time"

const (
	// TableNameUserLevel 是等级定义表。
	TableNameUserLevel = "user_levels"
	// TableNameUserLevelHistory 是等级变更流水（幂等键 event_key 唯一）。
	TableNameUserLevelHistory = "user_level_histories"
	// TableNameRechargePromotionTierRule 是充值阶梯赠送的可配置档位表。
	TableNameRechargePromotionTierRule = "recharge_promotion_tiers"
)

// 等级变更触发器。写进流水，便于回答「这一级是他自己消费升的，还是退款降的」。
const (
	UserLevelTriggerRecharge    = "recharge"    // 充值额变化后重新评估
	UserLevelTriggerConsumption = "consumption" // 消费额变化后重新评估
	UserLevelTriggerRefund      = "refund"      // 退款后重新评估（Q75-6：允许降级）
	UserLevelTriggerManual      = "manual"      // 管理员改等级 / 等级被删除后的回落
)

// UserLevel 是一个等级定义。
//
// 判定口径（service.ResolveUserLevel）：**同时**满足两个门槛才算达到该级，
// 取 rank 最高的达标级。因此
//   - 允许跳级（Q75-3）：一次达标可以直接跨到 rank 3，不要求先经过 rank 2；
//   - 等级数量无上限（Q75-7）：不设固定档数，也不允许出现「封顶级」的特殊分支。
//
// Threshold 为 0 表示「该维度不设门槛」，不是「必须为 0」。
type UserLevel struct {
	ID int32 `gorm:"primaryKey;autoIncrement" json:"id"`
	// Name 是展示名（如「基础会员」）。允许重名之外的任何中文名，但不允许空。
	Name string `gorm:"column:name;type:varchar(64);not null;default:''" json:"name"`
	// Rank 决定高低，**必须唯一**：两个同级 rank 会让「取最高达标级」变得不确定。
	// 用唯一索引在库层兜住，服务层同时做一次可读的校验（报出冲突的名字）。
	//
	// ⚠️ 列名是 level_rank 而不是 rank：**`RANK` 是 MySQL 8.0 的保留字**
	// （窗口函数 RANK() OVER ...）。叫 rank 的话 `ALTER TABLE ... ADD COLUMN rank int`
	// 与 `WHERE rank = ?` 在 MySQL 上都会语法错误，而 SQLite 测试库完全正常 ——
	// 本地全绿、生产启动即炸，是最难排查的一类。Go 字段名保持 Rank（JSON 也是 rank）。
	Rank int32 `gorm:"column:level_rank;type:int;not null;default:0;uniqueIndex:idx_user_level_rank" json:"rank"`
	// MinRechargeTotal 是累计充值门槛（充值成功金额之和，退款订单不计入）。
	MinRechargeTotal float64 `gorm:"column:min_recharge_total;type:decimal(18,6);not null;default:0" json:"minRechargeTotal"`
	// MinConsumptionTotal 是累计消费门槛（billing_records 的支出类之和）。
	MinConsumptionTotal float64 `gorm:"column:min_consumption_total;type:decimal(18,6);not null;default:0" json:"minConsumptionTotal"`
	// DeviceLimit 是设备池上限的**契约占位**（0 = 不限制）。
	//
	// C3（设备池）本轮不实现：这一列先建好并随等级一起下发，
	// 避免 C3 落地时再改一次表结构。当前没有任何业务读取它。
	DeviceLimit int32 `gorm:"column:device_limit;type:int;not null;default:0" json:"deviceLimit"`
	// Enabled=false 的等级不参与判定：既不参与「取最高达标级」，也不会被自动降到。
	//
	// ⚠️ 不能写 `default:true`：GORM 对带 default 的字段，在 Create 时会把**零值**
	// （bool 的 false）当作「没填」而替换成 default —— 结果是「新增一个停用等级」
	// 在库里被静默写成启用。这类静默翻转在生产上表现为「管理员点了停用，界面上
	// 也显示停用，但选档照样命中它」。所以这里用 not null 无默认值，
	// 由服务层显式写每一次 true/false。
	Enabled bool `gorm:"column:enabled;not null" json:"enabled"`
	// Description 是管理员备注，给运营看，不下发到用户端文案。
	Description string    `gorm:"column:description;type:varchar(255);not null;default:''" json:"description"`
	CreatedAt   time.Time `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
	UpdatedAt   time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

func (*UserLevel) TableName() string { return TableNameUserLevel }

// UserLevelHistory 是一次等级变更。
//
// 幂等锚点是 EventKey（唯一索引），不是「先查再写」：同一用户的同一
// 「from→to + trigger」只会落一行，重复触发靠库层唯一索引挡住，
// 事务重试或并发请求都不会写出双份流水。
type UserLevelHistory struct {
	ID          int32  `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID      int32  `gorm:"column:user_id;type:int(11);not null;index" json:"userId"`
	FromLevelID int32  `gorm:"column:from_level_id;type:int;not null;default:0" json:"fromLevelId"`
	ToLevelID   int32  `gorm:"column:to_level_id;type:int;not null;default:0" json:"toLevelId"`
	Trigger     string `gorm:"column:trigger;type:varchar(16);not null;default:''" json:"trigger"`
	// EventKey 形如 level:<userID>:<from>-<to>:<trigger>。
	EventKey string `gorm:"column:event_key;type:varchar(128);not null;default:'';uniqueIndex:idx_user_level_history_event" json:"eventKey"`
	// RechargeTotal / ConsumptionTotal 记录判定当时的两个口径值，
	// 事后可以回答「为什么他当时是 3 级」而不必重算历史账单。
	RechargeTotal    float64   `gorm:"column:recharge_total;type:decimal(18,6);not null;default:0" json:"rechargeTotal"`
	ConsumptionTotal float64   `gorm:"column:consumption_total;type:decimal(18,6);not null;default:0" json:"consumptionTotal"`
	Remark           string    `gorm:"column:remark;type:varchar(255);not null;default:''" json:"remark"`
	CreatedAt        time.Time `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
}

func (*UserLevelHistory) TableName() string { return TableNameUserLevelHistory }

// RechargePromotionTierRule 是充值阶梯赠送的一个可配置档位。
//
// 与 service.DefaultRechargePromotionTiers 的关系：表**空**时服务层回退到常量表，
// 因此上线不需要数据迁移、也不会改变任何既有发放行为（Q75-2 的可配置化前提）。
type RechargePromotionTierRule struct {
	ID int32 `gorm:"primaryKey;autoIncrement" json:"id"`
	// Threshold 是档位门槛（充值实付金额，2 位口径）。
	Threshold float64 `gorm:"column:threshold;type:decimal(18,6);not null;default:0;index" json:"threshold"`
	// Rate 是赠送比例（0.10 = 10%）。
	Rate float64 `gorm:"column:rate;type:decimal(18,6);not null;default:0" json:"rate"`
	// Enabled=false 的档位不参与选档，但行保留（便于临时停用再恢复）。
	//
	// ⚠️ 同 UserLevel.Enabled：不能写 `default:true`，否则「保存一个停用档位」
	// 会被 GORM 的零值替换成启用，而管理端列表读到的是真值 —— 表现为
	// 「界面上是停用，实际仍在发放」。
	Enabled   bool      `gorm:"column:enabled;not null" json:"enabled"`
	SortOrder int32     `gorm:"column:sort_order;type:int;not null;default:0" json:"sortOrder"`
	UpdatedAt time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

func (*RechargePromotionTierRule) TableName() string { return TableNameRechargePromotionTierRule }
