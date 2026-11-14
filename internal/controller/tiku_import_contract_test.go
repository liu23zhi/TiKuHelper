package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"

	"github.com/itihey/tikuAdapter/internal/dao"
)

// 契约：同一批题目分别走 JSON 导入与 CSV 导入，created / updated / skipped 三个计数必须完全相同。
//
// 为什么要有这条钉子（不是形式主义）：
//
// 两条路径的去重实现是**两份独立代码**，历史上口径已经漂移过一次 ——
// JSON 路径的 existing 判定只看 `item.ID > 0`，所以「id=0 但题库里已有同一道题」
// 会直接 Create 撞唯一索引 hash_course_idx，被渲染成 500「服务器错误」；
// 而 CSV 路径在同样输入下返回 skipped。运维在两种导入方式之间切换会撞上不同的结果。
//
// 这条用例把两条路径并排跑在**同一个库、同一批数据**上，逐计数比对。
// 任何一侧改去重口径而不同步另一侧，这里必红。
func TestJSONAndCSVImportProduceIdenticalCounts(t *testing.T) {
	gin.SetMode(gin.TestMode)

	type batch struct {
		name string
		// items 是「题目」的规范形态，两条路径都从这里派生各自格式的输入。
		items []importContractItem
	}

	cases := []batch{
		{
			name: "全新题目：两条路径都应 created",
			items: []importContractItem{
				{Question: "契约题一", Options: `["A","B"]`, Answer: `["A"]`},
				{Question: "契约题二", Options: `["A","B"]`, Answer: `["B"]`},
			},
		},
		{
			name: "同题重复出现于同一批：两条路径都应 skipped 掉后一条",
			items: []importContractItem{
				{Question: "契约重复题", Options: `["A","B"]`, Answer: `["A"]`},
				{Question: "契约重复题", Options: `["A","B"]`, Answer: `["A"]`},
			},
		},
		{
			name: "同 hash 不同答案：两条路径都不得 500（这是修掉的那个缺陷）",
			items: []importContractItem{
				{Question: "契约同键题", Options: `["A","B"]`, Answer: `["A"]`},
				{Question: "契约同键题", Options: `["A","B"]`, Answer: `["B"]`},
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// ⚠️ 两条路径必须各自跑在**独立子用例**里：newTikuControllerTestDB 用
			// t.Name() 生成 DSN，同一 t 上调两次会命中同一个 cache=shared 内存库，
			// 第二次看到的是第一次写入的行 —— 计数比对立刻失真（实测 CSV 前置行数=2）。
			var jsonCounts, csvCounts importContractCounts
			t.Run("JSON", func(t *testing.T) { jsonCounts = runJSONImportForContract(t, c.items) })
			t.Run("CSV", func(t *testing.T) { csvCounts = runCSVImportForContract(t, c.items) })

			t.Logf("JSON 计数: %+v", jsonCounts)
			t.Logf("CSV  计数: %+v", csvCounts)

			if jsonCounts.Created != csvCounts.Created {
				t.Errorf("created 不一致：JSON=%d CSV=%d —— 两条导入路径的去重口径已漂移",
					jsonCounts.Created, csvCounts.Created)
			}
			if jsonCounts.Updated != csvCounts.Updated {
				t.Errorf("updated 不一致：JSON=%d CSV=%d", jsonCounts.Updated, csvCounts.Updated)
			}
			if jsonCounts.Skipped != csvCounts.Skipped {
				t.Errorf("skipped 不一致：JSON=%d CSV=%d —— 同一批数据两种导入方式给出不同结果",
					jsonCounts.Skipped, csvCounts.Skipped)
			}
		})
	}
}

// 同 hash、库中已存在、未归档、id=0 时，JSON 路径必须 skipped 而不是 500。
//
// 单独再钉一次：上面的并排比对如果某天两边都坏成「都 500」，计数仍可能「一致」。
// 这条独立断言 request「不得返回 5xx」，是并排比对的第一道防线。
func TestJSONImportDuplicateWithoutIDReturnsSkippedNot500(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTikuControllerTestDB(t)
	ensureTikuControllerSourceColumn(t, db)
	user := seedTikuControllerUser(t, db, 1, "admin")

	payload := func(answer string) []byte {
		return []byte(fmt.Sprintf(
			`{"overwrite":false,"items":[{"id":0,"question":"同键探针","type":0,"options":"[\"A\",\"B\"]","answer":%q,"source_name":"人工导入"}]}`,
			answer))
	}

	first := postImportQuestions(t, user, payload("A"))
	if first.Code != http.StatusOK {
		t.Fatalf("首次导入应 200，实际 %d body=%s", first.Code, first.Body.String())
	}

	second := postImportQuestions(t, user, payload("B"))
	if second.Code != http.StatusOK {
		t.Fatalf("重复导入必须返回 200（skipped），实际 %d body=%s —— "+
			"原先这里会撞唯一索引 hash_course_idx 变成 500", second.Code, second.Body.String())
	}

	var resp struct {
		Created int `json:"created"`
		Updated int `json:"updated"`
		Skipped int `json:"skipped"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v body=%s", err, second.Body.String())
	}
	if resp.Created != 0 || resp.Updated != 0 || resp.Skipped != 1 {
		t.Fatalf("期望 created=0 updated=0 skipped=1，实际 %+v", resp)
	}

	var count int64
	if err := db.Raw("SELECT COUNT(*) FROM tiku WHERE question = ?", "同键探针").Row().Scan(&count); err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("库里应只有 1 行同题，实际 %d", count)
	}
}

type importContractItem struct {
	Question string
	Options  string
	Answer   string
}

type importContractCounts struct {
	Created int
	Updated int
	Skipped int
}

func postImportQuestions(t *testing.T, user any, payload []byte) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodPost, "/api/questions/import", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	ctx.Request = request
	ctx.Set("user", user)
	ImportQuestions(ctx)
	return recorder
}

func runJSONImportForContract(t *testing.T, items []importContractItem) importContractCounts {
	t.Helper()
	db := newTikuControllerTestDB(t)
	ensureTikuControllerSourceColumn(t, db)
	bindTikuContractDB(t, db)
	user := seedTikuControllerUser(t, db, 1, "admin")

	type payloadItem struct {
		ID         int32  `json:"id"`
		Question   string `json:"question"`
		Type       int32  `json:"type"`
		Options    string `json:"options"`
		Answer     string `json:"answer"`
		SourceName string `json:"source_name"`
	}
	payload := struct {
		Overwrite bool          `json:"overwrite"`
		Items     []payloadItem `json:"items"`
	}{Overwrite: false}
	for _, it := range items {
		payload.Items = append(payload.Items, payloadItem{
			ID: 0, Question: it.Question, Type: 0,
			Options: it.Options, Answer: it.Answer, SourceName: "人工导入",
		})
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("构造 JSON 载荷失败: %v", err)
	}

	rec := postImportQuestions(t, user, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("JSON 导入必须 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Created int `json:"created"`
		Updated int `json:"updated"`
		Skipped int `json:"skipped"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析 JSON 响应失败: %v body=%s", err, rec.Body.String())
	}
	return importContractCounts{Created: resp.Created, Updated: resp.Updated, Skipped: resp.Skipped}
}

func runCSVImportForContract(t *testing.T, items []importContractItem) importContractCounts {
	t.Helper()
	db := newTikuControllerTestDB(t)
	ensureTikuControllerSourceColumn(t, db)
	bindTikuContractDB(t, db)

	// CSV 表头与 service/export_csv.go 的 questions 列对齐。
	var buf bytes.Buffer
	buf.WriteString("question,type,options,answer,course_name,extra,source_name\n")
	for _, it := range items {
		// options / answer 是 JSON 字符串，放进 CSV 需要双引号包裹并转义内部双引号。
		csvEscape := func(s string) string {
			escaped := bytes.ReplaceAll([]byte(s), []byte(`"`), []byte(`""`))
			return `"` + string(escaped) + `"`
		}
		buf.WriteString(csvEscape(it.Question) + ",0," + csvEscape(it.Options) + "," +
			csvEscape(it.Answer) + ",,,人工导入\n")
	}

	result, err := service.ImportDataWithDB(db, service.ImportDataOptions{
		Resource: service.ImportResourceQuestions,
		Format:   "csv",
		Filename: "contract.csv",
		Content:  buf.Bytes(),
	})
	if err != nil {
		t.Fatalf("CSV 导入失败: %v", err)
	}
	return importContractCounts{
		Created: result.Succeeded,
		Updated: 0,
		Skipped: result.Skipped,
	}
}

// bindTikuContractDB 让 dao.Tiku 指向本次用例的库 —— JSON 路径走 dao，CSV 路径走显式 db 参数。
//
// newTikuControllerTestDB 内部已经调过 dao.SetDefault(db)，这里显式再调一次是为了
// 让「本用例断言的是哪个库」在调用点可读：JSON 与 CSV 两次必须各自绑定自己那个库，
// 否则 CSV 会把行写进 JSON 那个库，两侧计数比对立刻失去意义。
func bindTikuContractDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	dao.SetDefault(db)
}
