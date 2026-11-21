package service

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	legacyxls "github.com/extrame/xls"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"github.com/itihey/tikuAdapter/pkg/util"
	"github.com/xuri/excelize/v2"
	"gorm.io/gorm"
)

// 批量导入（107 Q13 / 111 §7 / 117 §4.5，任务书 ③④）。
//
// # 支持矩阵
//
//	.csv   ✅ 完整   标准库 encoding/csv
//	.json  ✅ 完整   标准库 encoding/json（题库）
//	.xlsx  ✅ 完整   excelize/v2（go.mod 已有 v2.11.1）
//	.xls   ✅ 完整   BIFF 解析器支持旧版 Excel 工作簿
//	.txt   ✅ 表格   按首行分隔符解析制表符或逗号分隔的题目表格
//	.xml   ✅ 完整   标准库 encoding/xml
//	.html  ✅ 表格   提取 HTML table 行列
//
// # 部分成功语义（107 Q13b 选 A）
//
// 一行失败不影响其它行，最终返回 成功 / 跳过 / 失败 三个计数 + 逐行明细。
// 绝不整体回滚：5000 行里 1 行手机号写错就让另外 4999 行白跑，运营会疯。
// 但**每行的写入本身是原子的**（单行的 Create/Update 在独立语句里），
// 不会出现「用户建出来了但余额没记」这类半成品。
//
// # 重复项（107 Q13c 选「跳过 + 报告」）
//
// 跳过必须**带原因**：只说 skipped 的数字，运营无法知道是「已存在」还是「本批内重复」。
// 两种原因的处置完全不同（前者不用管，后者说明文件本身有问题）。

// DefaultImportMaxRows 是单次导入的行数上限（107 Q13d：5000）。
const DefaultImportMaxRows = 5000

// QuestionImportMaxRows 是**题库**导入的单批行数上限，与默认上限同值。
//
// 单独命名而不是各处直接写 DefaultImportMaxRows：
// 题库是唯一「导出 → 改 → 重新导入」闭环的资源，它必须和 QuestionExportMaxRows
// （1 万，见 export_csv.go）保持固定倍数关系。两处各写一个字面量的话，
// 将来只改一边就会出现「导出 1 万、导入 5 千」，运营拿到文件要先手工拆批。
// 契约测试 TestQuestionImportExportLimitsAreConsistent 钉住这个关系。
//
// 这个上限对**所有格式**生效：CSV / JSON / xlsx / xls 都走 ImportDataWithDB
// 的第 621-628 行（同一处行数闸），不存在「JSON 能导 5 万、CSV 只能 5 千」。
const QuestionImportMaxRows = DefaultImportMaxRows

// 导入资源标识。与导出共用同一套命名（题库 questions / 黑名单 blacklist / 用户 users）。
const (
	ImportResourceQuestions      = ExportResourceQuestions
	ImportResourceBlacklist      = ExportResourceBlacklist
	ImportResourceWhitelist      = ExportResourceWhitelist
	ImportResourceUsers          = ExportResourceUsers
	ImportResourceIPLocations    = ExportResourceIPLocations
	ImportResourcePhoneLocations = ExportResourcePhoneLocations
)

// 导入格式。
const (
	ImportFormatCSV   = "csv"
	ImportFormatJSON  = "json"
	ImportFormatXlsx  = "xlsx"
	ImportFormatXls   = "xls"
	ImportFormatXML   = "xml"
	ImportFormatHTML  = "html"
	ImportFormatTXT   = "txt"
	ImportFormatUnset = ""
)

// 逐行结果。
const (
	ImportRowCreated = "created"
	ImportRowUpdated = "updated"
	ImportRowSkipped = "skipped"
	ImportRowFailed  = "failed"
)

// 导入错误。
var (
	// ErrImportResourceUnsupported 资源不在支持清单内（题库 / 黑名单 / 用户）。
	ErrImportResourceUnsupported = errors.New("不支持的导入资源")
	// ErrImportFormatUnsupported 格式无法识别（无扩展名 / 其它扩展名）。
	ErrImportFormatUnsupported = errors.New("不支持的导入格式")
	// ErrImportXlsUnsupported 表示文件内容不是可解析的 XLS 工作簿。
	ErrImportXlsUnsupported = errors.New("无法解析旧版 .xls 文件，请确认文件有效")
	// ErrImportFileEmpty 文件为空 / 只有表头。
	ErrImportFileEmpty = errors.New("导入文件没有数据行")
	// ErrImportHeaderInvalid 表头缺必填列。
	ErrImportHeaderInvalid = errors.New("导入文件的表头缺少必填列")
	// ErrImportWeakPasswordConfirmRequired 弱密码需要二次确认（任务书 ④）。
	ErrImportWeakPasswordConfirmRequired = errors.New("导入文件包含弱密码，请确认后重新提交")
	// ErrImportRowLimitExceeded 超过单次上限。
	ErrImportRowLimitExceeded = errors.New("导入行数超过单次上限")
)

// ImportDataOptions 是一次导入的参数。
type ImportDataOptions struct {
	// Resource 取 ImportResource* 之一。
	Resource string
	// Format 为空时按文件名/内容推断（DetectImportFormat）。
	Format string
	// Filename 用于推断格式（取扩展名）。
	Filename string
	// Content 是文件正文。
	Content []byte
	// Actor 是提交人（写进 users 台账 / 黑名单 created_by）。
	Actor entity.User
	// DryRun 只校验不落库（预览）。
	DryRun bool
	// ConfirmWeakPassword 弱密码二次确认（任务书 ④）。
	//
	// 注意方向：**弱密码不是拒绝**，是「需要多按一次确认」。
	// 管理员批量建测试号时就是想用简单密码，一律拒绝会把功能用死。
	ConfirmWeakPassword bool
	// Limit 为 0 时使用 DefaultImportMaxRows。
	Limit int
	// Now 为 nil 时用 time.Now（测试注入时钟）。
	Now func() time.Time
}

// ImportRowResult 是一行的处理结论。
type ImportRowResult struct {
	// Row 是**文件里的行号**（1 基，含表头；与 Excel/CSV 里看到的行号一致）。
	Row int `json:"row"`
	// Key 是该行的业务标识（黑名单的值 / 题库题面 / 用户名），用于对账。
	Key string `json:"key"`
	// Result 取 created / updated / skipped / failed。
	Result string `json:"result"`
	// Reason 是 skipped / failed 的原因。
	Reason string `json:"reason,omitempty"`
}

// ImportResult 是一次导入的完整结果。
type ImportResult struct {
	Total     int               `json:"total"`
	Succeeded int               `json:"succeeded"`
	Skipped   int               `json:"skipped"`
	Failed    int               `json:"failed"`
	Details   []ImportRowResult `json:"details"`
	// WeakPasswordRows 是检测到弱密码的行号（未确认时用于提示用户具体改哪几行）。
	WeakPasswordRows []int `json:"weakPasswordRows,omitempty"`
	// ErrorReport 是失败/跳过行的 CSV 报告（row,key,result,reason）。
	ErrorReport []byte `json:"-"`
}

// DetectImportFormat 按文件名扩展名 + 内容魔数推断格式。
//
// 扩展名优先，内容兜底：用户把 xlsx 改名成 .csv 时（很常见），
// 只看扩展名会用 CSV 解析器读 ZIP 二进制，报出一堆「第 N 行字段数不对」的噪音错误。
func DetectImportFormat(filename string, content []byte) (string, error) {
	lower := strings.ToLower(strings.TrimSpace(filename))
	switch {
	case strings.HasSuffix(lower, ".csv"):
		return ImportFormatCSV, nil
	case strings.HasSuffix(lower, ".json"):
		return ImportFormatJSON, nil
	case strings.HasSuffix(lower, ".xlsx"):
		return ImportFormatXlsx, nil
	case strings.HasSuffix(lower, ".xls"):
		return ImportFormatXls, nil
	case strings.HasSuffix(lower, ".xml"):
		return ImportFormatXML, nil
	case strings.HasSuffix(lower, ".html"), strings.HasSuffix(lower, ".htm"):
		return ImportFormatHTML, nil
	case strings.HasSuffix(lower, ".txt"):
		return ImportFormatTXT, nil
	}
	// 扩展名缺失或不认识 → 看内容。
	if len(content) == 0 {
		return ImportFormatUnset, ErrImportFormatUnsupported
	}
	if len(content) >= 2 && content[0] == 'P' && content[1] == 'K' {
		return ImportFormatXlsx, nil
	}
	if len(content) >= 8 && bytes.Equal(content[:8], ole2Magic) {
		return ImportFormatXls, nil
	}
	trimmed := bytes.TrimSpace(content)
	if len(trimmed) > 0 {
		switch trimmed[0] {
		case '{', '[':
			return ImportFormatJSON, nil
		}
		if looksLikeHTMLTable(content) {
			return ImportFormatHTML, nil
		}
		if bytes.HasPrefix(trimmed, []byte("<?xml")) || bytes.HasPrefix(trimmed, []byte("<questions")) {
			return ImportFormatXML, nil
		}
	}
	return ImportFormatUnset, fmt.Errorf("%w：请选择 .json / .xlsx / .xls / .csv / .xml / .html / .txt 文件", ErrImportFormatUnsupported)
}

// ole2Magic 是 OLE2 复合文档（真 .xls 的容器）的 8 字节魔数。
var ole2Magic = []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}

// htmlTableMarkers 用于识别「Excel 另存为单一文件网页」得到的伪 .xls。
var htmlTableMarkers = [][]byte{
	[]byte("<table"),
	[]byte("<TABLE"),
	[]byte("<html"),
	[]byte("<HTML"),
}

// looksLikeHTMLTable 判断内容是不是 HTML 表格（伪 .xls）。
func looksLikeHTMLTable(content []byte) bool {
	for _, marker := range htmlTableMarkers {
		if bytes.Contains(content, marker) {
			return true
		}
	}
	return false
}

// ParseImportRows 把文件解析成「表头 + 数据行」。
//
// 返回的 rows 不含表头；每个元素的长度等于表头长度（缺列补空串）。
// 这一层只做**格式解析**，不做任何业务校验 —— 业务规则属于各资源的 importXxxRows。
func ParseImportRows(format string, content []byte) ([]string, [][]string, error) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case ImportFormatCSV:
		return parseImportCSV(content)
	case ImportFormatJSON:
		return parseImportJSON(content)
	case ImportFormatXlsx:
		return parseImportXlsx(content)
	case ImportFormatXls:
		return parseLegacyXls(content)
	case ImportFormatXML:
		return parseImportXML(content)
	case ImportFormatHTML:
		return parseImportHTMLTable(content)
	case ImportFormatTXT:
		return parseImportTextTable(content)
	default:
		return nil, nil, fmt.Errorf("%w：%q", ErrImportFormatUnsupported, format)
	}
}

// parseImportTextTable accepts structured UTF-8 TSV or CSV text files. It does
// not guess at free-form prose, so every accepted TXT file has an explicit header.
func parseImportTextTable(content []byte) ([]string, [][]string, error) {
	text := bytes.TrimPrefix(content, []byte("\uFEFF"))
	if len(bytes.TrimSpace(text)) == 0 {
		return nil, nil, ErrImportFileEmpty
	}
	firstLine := text
	if newline := bytes.IndexByte(firstLine, '\n'); newline >= 0 {
		firstLine = firstLine[:newline]
	}
	delimiter := rune(',')
	if bytes.Contains(firstLine, []byte("\t")) {
		delimiter = '\t'
	}
	reader := csv.NewReader(bytes.NewReader(text))
	reader.Comma = delimiter
	reader.FieldsPerRecord = 0
	records, err := reader.ReadAll()
	if err != nil {
		return nil, nil, fmt.Errorf("解析 TXT 表格失败：%w", err)
	}
	return normalizeImportTable(records)
}

// parseImportCSV 解析 CSV。
//
// 三个兼容点（都是真实文件里出现过的）：
//   - **BOM**：导出带 UTF-8 BOM，导入必须吃掉它，否则第一列表头叫 "\uFEFFvalue"，
//     「缺少必填列 value」查半天查不出来；
//   - **CRLF**：Windows 上另存的 CSV 行尾是 \r\n，encoding/csv 会保留 \r 到最后一列；
//   - **行尾空行**：末尾多一个空行会让「为空」判断失效。
func parseImportCSV(content []byte) ([]string, [][]string, error) {
	reader := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(content, []byte("\uFEFF"))))
	// 不允许字段数不一致：CSV 里字段数不对就是文件坏了，
	// 强行 pad 会把「少了一列密码」的账号静默建成空密码账号。
	reader.FieldsPerRecord = 0
	records, err := reader.ReadAll()
	if err != nil {
		return nil, nil, fmt.Errorf("解析 CSV 失败：%w", err)
	}
	return normalizeImportTable(records)
}

// normalizeImportTable 把 records（含表头）整理成表头 + 数据行，并去掉全空行。
func normalizeImportTable(records [][]string) ([]string, [][]string, error) {
	// 丢掉前导空行与尾部空行（Excel 常留下几行全空的尾巴）。
	cleaned := make([][]string, 0, len(records))
	for _, record := range records {
		if isBlankImportRow(record) {
			continue
		}
		cleaned = append(cleaned, record)
	}
	if len(cleaned) == 0 {
		return nil, nil, ErrImportFileEmpty
	}
	header := make([]string, len(cleaned[0]))
	for i, cell := range cleaned[0] {
		header[i] = strings.TrimSpace(strings.TrimPrefix(cell, "\uFEFF"))
	}
	rows := make([][]string, 0, len(cleaned)-1)
	for _, record := range cleaned[1:] {
		cells := make([]string, len(header))
		for i := range header {
			if i < len(record) {
				cells[i] = strings.TrimSpace(strings.TrimSuffix(record[i], "\r"))
			}
		}
		rows = append(rows, cells)
	}
	if len(rows) == 0 {
		return header, nil, ErrImportFileEmpty
	}
	return header, rows, nil
}

// isBlankImportRow 判断整行是否全空。
func isBlankImportRow(record []string) bool {
	for _, cell := range record {
		if strings.TrimSpace(strings.TrimSuffix(cell, "\r")) != "" {
			return false
		}
	}
	return true
}

// parseImportJSON 解析题库 JSON。
//
// 接受两种形态（都是既有前端与接口实际会产生的）：
//
//	{"items":[{question,type,options,answer,...}]}   —— Go 接口 /questions/import 的请求体
//	[{question,type,options,answer,...}]             —— 前端 handleExportSelected 导出的数组
//
// options / answer 允许是数组，也允许是 JSON 字符串（CSV 回灌的那一列）——
// 「CSV 导出 → JSON 导入」这种交叉用法很自然，没必要拒绝。
func parseImportJSON(content []byte) ([]string, [][]string, error) {
	trimmed := bytes.TrimSpace(bytes.TrimPrefix(content, []byte("\uFEFF")))
	if len(trimmed) == 0 {
		return nil, nil, ErrImportFileEmpty
	}
	var rawList []json.RawMessage
	if trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &rawList); err != nil {
			return nil, nil, fmt.Errorf("解析 JSON 数组失败：%w", err)
		}
	} else {
		var envelope struct {
			Items []json.RawMessage `json:"items"`
		}
		if err := json.Unmarshal(trimmed, &envelope); err != nil {
			return nil, nil, fmt.Errorf("解析 JSON 对象失败：%w", err)
		}
		rawList = envelope.Items
	}
	if len(rawList) == 0 {
		return nil, nil, ErrImportFileEmpty
	}

	header := []string{"question", "type", "options", "answer", "course_name", "extra", "source_name"}
	rows := make([][]string, 0, len(rawList))
	for _, raw := range rawList {
		var item struct {
			Question   string          `json:"question"`
			Type       json.RawMessage `json:"type"`
			Options    json.RawMessage `json:"options"`
			Answer     json.RawMessage `json:"answer"`
			CourseName string          `json:"course_name"`
			Extra      string          `json:"extra"`
			SourceName string          `json:"source_name"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, nil, fmt.Errorf("解析 JSON 行失败：%w", err)
		}
		rows = append(rows, []string{
			item.Question,
			jsonScalarText(item.Type),
			jsonArrayOrScalarText(item.Options),
			jsonArrayOrScalarText(item.Answer),
			item.CourseName,
			item.Extra,
			item.SourceName,
		})
	}
	return header, rows, nil
}

// jsonScalarText 把 JSON 数字/字符串统一成文本（type 字段）。
func jsonScalarText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return strings.TrimSpace(asString)
	}
	var asNumber json.Number
	if err := json.Unmarshal(raw, &asNumber); err == nil {
		return asNumber.String()
	}
	return strings.TrimSpace(string(raw))
}

// jsonArrayOrScalarText 把 options / answer 统一成 JSON 字符串（CSV 单格形态）。
//
// 已经是字符串时原样返回（用户可能直接给了 `["A","B"]` 这样的字符串），
// 是数组时重新序列化一遍（保证 CSV 里存的是标准 JSON）。
func jsonArrayOrScalarText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "[]"
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		trimmed := strings.TrimSpace(asString)
		if trimmed == "" {
			return "[]"
		}
		return trimmed
	}
	var asArray []string
	if err := json.Unmarshal(raw, &asArray); err == nil {
		encoded, err := json.Marshal(asArray)
		if err != nil {
			return "[]"
		}
		return string(encoded)
	}
	return "[]"
}

// parseImportXlsx 用 excelize 解析第一个工作表。
func parseImportXlsx(content []byte) ([]string, [][]string, error) {
	file, err := excelize.OpenReader(bytes.NewReader(content))
	if err != nil {
		return nil, nil, fmt.Errorf("解析 xlsx 失败：%w", err)
	}
	defer func() { _ = file.Close() }()
	sheets := file.GetSheetList()
	if len(sheets) == 0 {
		return nil, nil, ErrImportFileEmpty
	}
	records, err := file.GetRows(sheets[0])
	if err != nil {
		return nil, nil, fmt.Errorf("读取 xlsx 工作表失败：%w", err)
	}
	return normalizeImportTable(records)
}

// parseLegacyXls handles real OLE2/BIFF workbooks, ZIP-based XLSX files
// mislabeled as .xls, and HTML tables saved with an .xls suffix.
func parseLegacyXls(content []byte) ([]string, [][]string, error) {
	if len(content) == 0 {
		return nil, nil, fmt.Errorf("%w：%w", ErrImportXlsUnsupported, ErrImportFileEmpty)
	}
	if len(content) >= 2 && content[0] == 'P' && content[1] == 'K' {
		return parseImportXlsx(content)
	}
	if looksLikeHTMLTable(content) {
		return parseImportHTMLTable(content)
	}
	if len(content) >= 8 && bytes.Equal(content[:8], ole2Magic) {
		workbook, err := legacyxls.OpenReader(bytes.NewReader(content), "utf-8")
		if err != nil {
			return nil, nil, fmt.Errorf("解析 xls 失败：%w", err)
		}
		if workbook.NumSheets() == 0 {
			return nil, nil, ErrImportFileEmpty
		}
		sheet := workbook.GetSheet(0)
		if sheet == nil {
			return nil, nil, ErrImportFileEmpty
		}
		rows := make([][]string, 0, int(sheet.MaxRow)+1)
		for rowIndex := 0; rowIndex <= int(sheet.MaxRow); rowIndex++ {
			row := sheet.Row(rowIndex)
			if row == nil {
				rows = append(rows, nil)
				continue
			}
			cells := make([]string, row.LastCol()+1)
			for columnIndex := range cells {
				cells[columnIndex] = row.Col(columnIndex)
			}
			rows = append(rows, cells)
		}
		return normalizeImportTable(rows)
	}
	return nil, nil, ErrImportXlsUnsupported
}

// parseImportXML parses a question list in either a <questions> root or as
// repeated <question> records. Each question's child elements map to the same
// normalized import columns used by CSV and JSON.
func parseImportXML(content []byte) ([]string, [][]string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(content))
	decoder.Strict = true
	var document struct {
		Items []struct {
			Question   string `xml:"question"`
			Type       string `xml:"type"`
			Options    string `xml:"options"`
			Answer     string `xml:"answer"`
			CourseName string `xml:"course_name"`
			Extra      string `xml:"extra"`
			SourceName string `xml:"source_name"`
		} `xml:"question"`
		Rows []struct {
			Question   string `xml:"question"`
			Type       string `xml:"type"`
			Options    string `xml:"options"`
			Answer     string `xml:"answer"`
			CourseName string `xml:"course_name"`
			Extra      string `xml:"extra"`
			SourceName string `xml:"source_name"`
		} `xml:"row"`
	}
	if err := decoder.Decode(&document); err != nil {
		return nil, nil, fmt.Errorf("解析 XML 失败：%w", err)
	}
	items := document.Items
	if len(items) == 0 {
		items = document.Rows
	}
	if len(items) == 0 {
		return nil, nil, ErrImportFileEmpty
	}
	rows := make([][]string, 0, len(items))
	for _, item := range items {
		rows = append(rows, []string{item.Question, item.Type, item.Options, item.Answer, item.CourseName, item.Extra, item.SourceName})
	}
	return []string{"question", "type", "options", "answer", "course_name", "extra", "source_name"}, rows, nil
}

// parseImportHTMLTable 从 HTML 表格里抽出行（伪 .xls）。
//
// 只支持最基本的 `<tr><td>` 形态（Excel 的单一文件网页就是这一种），
// 不引入 HTML 解析库：目标文件结构固定，正则足够，且避免为一个边缘分支拉依赖。
// 单元格文本去标签 + 解最常见几个实体。
//
// ⚠️ 实现纪律：所有下标都在**同一个不断前移的字符串**上算。
// 早先的版本用「小写副本上算下标、原串上切片」——前移之后两串的偏移不再对应，
// 表现为表头行被重复当成数据行（解析出错误的行数与内容），很难从错误信息看出来。
func parseImportHTMLTable(content []byte) ([]string, [][]string, error) {
	remaining := string(content)
	records := make([][]string, 0, 16)
	for {
		lowered := strings.ToLower(remaining)
		trStart := strings.Index(lowered, "<tr")
		if trStart < 0 {
			break
		}
		trEnd := strings.Index(lowered[trStart:], "</tr>")
		if trEnd < 0 {
			break
		}
		segment := remaining[trStart : trStart+trEnd]
		remaining = remaining[trStart+trEnd+len("</tr>"):]

		cells := make([]string, 0, 8)
		for {
			cellStart := indexOfAny(strings.ToLower(segment), "<td", "<th")
			if cellStart < 0 {
				break
			}
			openEnd := strings.Index(segment[cellStart:], ">")
			if openEnd < 0 {
				break
			}
			bodyStart := cellStart + openEnd + 1
			loweredBody := strings.ToLower(segment[bodyStart:])
			cellEnd := indexOfAny(loweredBody, "</td", "</th")
			if cellEnd < 0 {
				break
			}
			cells = append(cells, decodeImportHTMLEntities(stripImportHTMLTags(segment[bodyStart:bodyStart+cellEnd])))
			// 跳过这个单元格的结束标签，避免再次命中同一个 </td。
			closingEnd := strings.Index(segment[bodyStart+cellEnd:], ">")
			if closingEnd < 0 {
				break
			}
			segment = segment[bodyStart+cellEnd+closingEnd+1:]
		}
		if len(cells) > 0 {
			records = append(records, cells)
		}
	}
	if len(records) == 0 {
		return nil, nil, ErrImportXlsUnsupported
	}
	return normalizeImportTable(records)
}

// indexOfAny 返回多个子串中最早出现的下标（都找不到返回 -1）。
func indexOfAny(text string, subs ...string) int {
	best := -1
	for _, sub := range subs {
		if index := strings.Index(text, sub); index >= 0 && (best < 0 || index < best) {
			best = index
		}
	}
	return best
}

// stripImportHTMLTags 去掉内嵌标签（Excel 会包 <font>/<span>）。
func stripImportHTMLTags(text string) string {
	var builder strings.Builder
	depth := 0
	for _, ch := range text {
		switch ch {
		case '<':
			depth++
		case '>':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				builder.WriteRune(ch)
			}
		}
	}
	return builder.String()
}

// decodeImportHTMLEntities 还原最常见的 4 个 HTML 实体。
func decodeImportHTMLEntities(text string) string {
	replacer := strings.NewReplacer(
		"&amp;", "&",
		"&lt;", "<",
		"&gt;", ">",
		"&nbsp;", " ",
		"&quot;", `"`,
		"&#39;", "'",
	)
	return strings.TrimSpace(replacer.Replace(text))
}

// importColumnIndex 建「列名 → 下标」映射。
//
// 列名比较**大小写不敏感且忽略空白**：运营手写 CSV 时 `Value` / ` value ` 很常见，
// 因为这些差异就报「缺少必填列」是纯粹的折磨。
func importColumnIndex(header []string) map[string]int {
	index := make(map[string]int, len(header))
	for i, name := range header {
		key := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(name), " ", ""))
		if key == "" {
			continue
		}
		if _, exists := index[key]; !exists {
			index[key] = i
		}
	}
	return index
}

// requireImportColumns 校验必填列都在，缺哪个就报哪个（不要笼统说「表头无效」）。
func requireImportColumns(index map[string]int, required []string) error {
	missing := make([]string, 0, len(required))
	for _, name := range required {
		if _, ok := index[strings.ToLower(name)]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w：缺少 %s", ErrImportHeaderInvalid, strings.Join(missing, "、"))
	}
	return nil
}

// importCell 读一列的值（列不存在时返回空串）。
func importCell(row []string, index map[string]int, column string) string {
	position, ok := index[strings.ToLower(column)]
	if !ok || position < 0 || position >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[position])
}

// ImportDataWithDB 执行一次导入。
//
// 流程：解析 → 行数上限 → 弱密码检查（users）→ 逐行落库 → 生成错误报告。
//
// 错误语义分两类：
//   - **整体性错误**（格式不识别 / 表头缺列 / 超上限 / 弱密码未确认）→ 返回 error，
//     一行都不写；
//   - **逐行错误**（手机号非法 / 用户名已存在）→ 计入 ImportResult.Failed / Skipped，
//     返回的 error 为 nil。
//
// 这个分界是刻意的：前者说明「文件本身没法用」，后者说明「文件可用，个别行有问题」。
func ImportDataWithDB(db *gorm.DB, opts ImportDataOptions) (ImportResult, error) {
	if db == nil {
		return ImportResult{}, fmt.Errorf("数据库未初始化")
	}
	resource := strings.ToLower(strings.TrimSpace(opts.Resource))
	switch resource {
	case ImportResourceQuestions, ImportResourceBlacklist, ImportResourceWhitelist, ImportResourceUsers, ImportResourceIPLocations, ImportResourcePhoneLocations, ImportResourceSensitiveImages, ImportResourceOCRCache:
	default:
		return ImportResult{}, fmt.Errorf("%w: %q", ErrImportResourceUnsupported, opts.Resource)
	}
	format := strings.ToLower(strings.TrimSpace(opts.Format))
	if format == "" {
		detected, err := DetectImportFormat(opts.Filename, opts.Content)
		if err != nil {
			return ImportResult{}, err
		}
		format = detected
	}
	if resource == ImportResourceIPLocations || resource == ImportResourcePhoneLocations {
		return importManagementLocations(db, opts, resource, format)
	}
	if resource == ImportResourceSensitiveImages {
		return importSensitiveImages(db, opts, format)
	}
	if resource == ImportResourceOCRCache {
		return importOCRCacheRows(db, opts, format)
	}
	if resource == ImportResourceBlacklist || resource == ImportResourceWhitelist {
		return importManagementRules(db, opts, resource, format)
	}
	header, rows, err := ParseImportRows(format, opts.Content)
	if err != nil {
		return ImportResult{}, err
	}
	limit := importResolveLimit(resource, opts.Limit)
	if limit > 0 && len(rows) > limit {
		return ImportResult{}, fmt.Errorf("%w：单次最多 %d 行，当前 %d 行，请分批导入",
			ErrImportRowLimitExceeded, limit, len(rows))
	}

	index := importColumnIndex(header)
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}

	switch resource {
	case ImportResourceQuestions:
		return importQuestionRows(db, opts, index, rows)
	default:
		return importUserRows(db, opts, index, rows, now)
	}
}

// inferBlacklistKindForImport 按值的形态推断 kind（导入侧，**只认无歧义的两类**）。
//
// 与导出侧的 blacklistKindOfExportValue 有意不同：导出只是给值打码，
// 猜错最多是打码过度；导入是**写规则**，猜错会把一条永远匹配不到的规则塞进库里，
// 而运营看到「导入成功」就以为已经拦住了。
//
// 因此这里只接受：
//   - 含 `/` 或 `:` 或三个点 → ip（CIDR / IPv6 / IPv4）；
//   - 11 位纯数字 → phone；
//   - 其余 → 报错，要求显式填 kind 列（避免把 `abc` 当成 OpenID）。
func inferBlacklistKindForImport(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", fmt.Errorf("value 不能为空")
	}
	if strings.Contains(trimmed, "/") || strings.Contains(trimmed, ":") || strings.Count(trimmed, ".") == 3 {
		return BlacklistKindIP, nil
	}
	if len([]rune(trimmed)) == 11 && strings.Trim(trimmed, "0123456789") == "" {
		return BlacklistKindPhone, nil
	}
	return "", fmt.Errorf("无法从值推断类型，请补填 kind 列（ip / phone / wechat）")
}

// isDuplicateBlacklistError 判断错误是不是「已存在相同规则」。
//
// 不靠错误字符串匹配（GORM / SQLite / MySQL 的文案各不相同），而是按错误出现后
// 表里确实有这条规则来判断 —— 但这需要再查一次库，成本高且可能与并发冲突。
// 折中：只看唯一约束类的错误特征，其余一律按 failed 报出来（不掩盖真错误）。
func isDuplicateBlacklistError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	for _, marker := range []string{"unique constraint", "duplicate entry", "already exists", "unique constraint failed"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// parseImportExpiresAt 解析过期时间列。
//
// 接受 `YYYY-MM-DD`（按当天 23:59:59 本地时区，与黑名单管理页同一口径）
// 与 RFC3339。空串 = 永久。
func parseImportExpiresAt(value string) (*time.Time, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, nil
	}
	if parsed, err := time.Parse(time.RFC3339, trimmed); err == nil {
		return &parsed, nil
	}
	if parsed, err := time.ParseInLocation("2006-01-02", trimmed, time.Local); err == nil {
		end := parsed.AddDate(0, 0, 1).Add(-time.Second)
		return &end, nil
	}
	return nil, fmt.Errorf("过期时间格式无效（应为 YYYY-MM-DD）")
}

// QuestionImportDedupeKey 返回题库去重键 —— 与 entity.Tiku 的唯一索引
// hash_course_idx（hash + course_name）**逐字对应**。
//
// 这是「JSON 导入与 CSV 导入口径一致」的唯一实现点：
//
//   - JSON 路径（controller.ImportQuestions）最终也落到同一个唯一索引上；
//   - CSV 路径（本文件的 importQuestionRows）用本函数判定「库内已存在」与「本批内重复」。
//
// 因此键里**必须同时有 hash 和 course_name**。只用 hash 会把「同一道题挂在不同课程下」
// 误判成重复 —— 那是唯一索引允许存的两种行，跳过它就等于导入静默丢题。
//
// 分隔符用 NUL：hash 是 32 位十六进制、课程名是任意文本，NUL 是唯一不可能
// 自然出现在两者拼接边界上的字符（用 "|" 之类的可打印符时，"a|b"+"c" 与 "a"+"b|c"
// 会撞成同一个键）。
func QuestionImportDedupeKey(hash, courseName string) string {
	return hash + "\x00" + courseName
}

// ImportRowLimitOf 返回某资源的导入行数上限（供 HTTP 层与前端显示「最多 N 行」）。
//
// 与导出侧 ExportRowLimitOf 对称：两侧上限必须能被同一段代码问出来，
// 否则前端只能各自硬编码数字，改一处漏一处。
func ImportRowLimitOf(resource string) int {
	switch strings.ToLower(strings.TrimSpace(resource)) {
	case ImportResourceQuestions:
		return QuestionImportMaxRows
	default:
		return DefaultImportMaxRows
	}
}

// importResolveLimit 把「调用方请求的 limit」解析成「实际生效的 limit」。
//
// 规则与导出侧 exportResolveLimit 完全对称（同一套语义，不该有两套）：
//  1. requested == 0 → 资源上限；
//  2. requested < 0  → 资源上限（"不限制"只能到上限，不能变成无上限）；
//  3. requested > 上限 → 上限。
//
// 唯一的例外是**收紧**：requested 小于上限时按 requested 走（测试里 Limit:5 就是靠这条）。
func importResolveLimit(resource string, requested int) int {
	ceiling := ImportRowLimitOf(resource)
	if requested <= 0 || requested > ceiling {
		return ceiling
	}
	return requested
}

// importQuestionRows 导入题库（列与导出一致，Q12-i「导出列 = 可导入字段」）。
//
// 去重口径与 /questions/import 完全一致：唯一键是 (hash, course_name)，
// hash 由 util.BuildTikuHash 算（写入与查询必须同一个公式，见 FillHash 的注释）。
// 键的构造只允许走 QuestionImportDedupeKey，不要在循环里另写一遍拼接。
//
// 关于 CSV 的 hash 列：它是**导出用的核对列**，导入时一律重算、绝不采信文件里的值。
// 采信它等于把「文件里写的 hash」当成唯一索引的依据 —— 一个手改过的 CSV 可以让
// 两条不同题目落到同一个 hash 上，或者让同一道题绕过去重。
func importQuestionRows(db *gorm.DB, opts ImportDataOptions, index map[string]int, rows [][]string) (ImportResult, error) {
	if err := requireImportColumns(index, []string{"question"}); err != nil {
		return ImportResult{}, err
	}
	result := ImportResult{Total: len(rows), Details: make([]ImportRowResult, 0, len(rows))}
	seen := make(map[string]int, len(rows))

	for i, row := range rows {
		rowNo := i + 2
		question := importCell(row, index, "question")
		entry := ImportRowResult{Row: rowNo, Key: truncateImportKey(question)}
		if question == "" {
			entry.Result = ImportRowFailed
			entry.Reason = "question 不能为空"
			result.Details = append(result.Details, entry)
			result.Failed++
			continue
		}
		optionsJSON := importCell(row, index, "options")
		if optionsJSON == "" {
			optionsJSON = "[]"
		}
		answerJSON := importCell(row, index, "answer")
		if answerJSON == "" {
			answerJSON = "[]"
		}
		// 两列都必须是合法 JSON 数组：CSV 手工编辑时最常见的错误是用了中文逗号或忘了引号。
		options, optionsErr := decodeImportStringArray(optionsJSON)
		if optionsErr != nil {
			entry.Result = ImportRowFailed
			entry.Reason = "options 不是合法的 JSON 数组"
			result.Details = append(result.Details, entry)
			result.Failed++
			continue
		}
		if _, answerErr := decodeImportStringArray(answerJSON); answerErr != nil {
			entry.Result = ImportRowFailed
			entry.Reason = "answer 不是合法的 JSON 数组"
			result.Details = append(result.Details, entry)
			result.Failed++
			continue
		}
		questionType := 0
		if rawType := importCell(row, index, "type"); rawType != "" {
			parsed, parseErr := strconv.Atoi(rawType)
			if parseErr != nil {
				entry.Result = ImportRowFailed
				entry.Reason = "type 必须是数字"
				result.Details = append(result.Details, entry)
				result.Failed++
				continue
			}
			questionType = parsed
		}
		courseName := importCell(row, index, "course_name")
		extra := importCell(row, index, "extra")
		sourceName := importCell(row, index, "source_name")
		if sourceName == "" {
			sourceName = "人工导入"
		}
		hash := util.BuildTikuHash(question, options, questionType)

		seenKey := QuestionImportDedupeKey(hash, courseName)
		if firstRow, duplicated := seen[seenKey]; duplicated {
			entry.Result = ImportRowSkipped
			entry.Reason = fmt.Sprintf("本批内重复（第 %d 行已处理）", firstRow)
			result.Details = append(result.Details, entry)
			result.Skipped++
			continue
		}
		seen[seenKey] = rowNo

		if opts.DryRun {
			entry.Result = ImportRowCreated
			entry.Reason = "预览模式，未落库"
			result.Details = append(result.Details, entry)
			result.Succeeded++
			continue
		}

		var existing int64
		if err := db.Model(&entity.Tiku{}).
			Where("hash = ? AND course_name = ?", hash, courseName).
			Count(&existing).Error; err != nil {
			return result, fmt.Errorf("查询题库失败: %w", err)
		}
		if existing > 0 {
			// 「已存在」按 skipped 处理（107 Q13c：跳过 + 报告），不覆盖。
			// 覆盖是单独的能力（overwrite 参数），批量导入默认不该改历史答案。
			entry.Result = ImportRowSkipped
			entry.Reason = "已存在相同题目（hash + course_name）"
			result.Details = append(result.Details, entry)
			result.Skipped++
			continue
		}
		item := &entity.Tiku{
			Question:   question,
			Type:       int32(questionType),
			Options:    optionsJSON,
			Answer:     answerJSON,
			Hash:       hash,
			CourseName: courseName,
			Extra:      extra,
			SourceName: sourceName,
		}
		if err := db.Create(item).Error; err != nil {
			if isDuplicateBlacklistError(err) {
				entry.Result = ImportRowSkipped
				entry.Reason = "已存在相同题目（唯一索引）"
				result.Details = append(result.Details, entry)
				result.Skipped++
				continue
			}
			entry.Result = ImportRowFailed
			entry.Reason = err.Error()
			result.Details = append(result.Details, entry)
			result.Failed++
			continue
		}
		entry.Result = ImportRowCreated
		result.Details = append(result.Details, entry)
		result.Succeeded++
	}
	result.ErrorReport = buildImportErrorReport(result.Details)
	return result, nil
}

// decodeImportStringArray 解 JSON 字符串数组。
func decodeImportStringArray(raw string) ([]string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	var values []string
	if err := json.Unmarshal([]byte(trimmed), &values); err != nil {
		return nil, err
	}
	return values, nil
}

// truncateImportKey 把题面截断成对账用的短键（避免明细里塞进 2000 字题面）。
func truncateImportKey(value string) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= 40 {
		return string(runes)
	}
	return string(runes[:40]) + "…"
}

// importUserRows 导入用户（111 §7.4 的字段设计）。
//
// 弱密码处理（任务书 ④）：检测到弱密码**先不写任何一行**，返回
// ErrImportWeakPasswordConfirmRequired + 具体行号；调用方拿到确认后再传
// ConfirmWeakPassword=true 重跑一次。
//
// 为什么不在同一个请求里「边问边写」：导入是异步任务，任务无法在执行中途
// 停下来等管理员点确认。所以二次确认发生在**提交之前**（同步校验阶段），
// 那时还没有任何写入，确认后重跑是干净的一次导入。
func importUserRows(db *gorm.DB, opts ImportDataOptions, index map[string]int, rows [][]string, now func() time.Time) (ImportResult, error) {
	if err := requireImportColumns(index, []string{"username", "password"}); err != nil {
		return ImportResult{}, err
	}
	// 先整体扫一遍弱密码：必须在写任何一行之前完成，否则「确认后重跑」会撞唯一键。
	weakRows := make([]int, 0, 4)
	for i, row := range rows {
		password := importCell(row, index, "password")
		username := importCell(row, index, "username")
		if username == "" || password == "" {
			continue
		}
		if IsWeakImportPassword(password) {
			weakRows = append(weakRows, i+2)
		}
	}
	if len(weakRows) > 0 && !opts.ConfirmWeakPassword {
		return ImportResult{
				Total:            len(rows),
				WeakPasswordRows: weakRows,
			}, fmt.Errorf("%w：共 %d 行（第 %s 行）", ErrImportWeakPasswordConfirmRequired,
				len(weakRows), joinImportRowNumbers(weakRows))
	}

	result := ImportResult{Total: len(rows), Details: make([]ImportRowResult, 0, len(rows))}
	seen := make(map[string]int, len(rows))
	for i, row := range rows {
		rowNo := i + 2
		username := importCell(row, index, "username")
		password := importCell(row, index, "password")
		entry := ImportRowResult{Row: rowNo, Key: username}
		switch {
		case username == "":
			entry.Result = ImportRowFailed
			entry.Reason = "username 不能为空"
			result.Details = append(result.Details, entry)
			result.Failed++
			continue
		case password == "":
			entry.Result = ImportRowFailed
			entry.Reason = "password 不能为空"
			result.Details = append(result.Details, entry)
			result.Failed++
			continue
		}
		if err := util.ValidateBcryptPasswordBytes(password); err != nil {
			entry.Result = ImportRowFailed
			entry.Reason = fmt.Sprintf("密码超过 %d 字节", util.BcryptMaxPasswordBytes)
			result.Details = append(result.Details, entry)
			result.Failed++
			continue
		}
		normalizedUsername := strings.ToLower(strings.TrimSpace(username))
		if firstRow, duplicated := seen[normalizedUsername]; duplicated {
			entry.Result = ImportRowSkipped
			entry.Reason = fmt.Sprintf("本批内重复（第 %d 行已处理）", firstRow)
			result.Details = append(result.Details, entry)
			result.Skipped++
			continue
		}
		seen[normalizedUsername] = rowNo

		if opts.DryRun {
			entry.Result = ImportRowCreated
			entry.Reason = "预览模式，未落库"
			result.Details = append(result.Details, entry)
			result.Succeeded++
			continue
		}

		var existing int64
		if err := db.Model(&entity.User{}).Where("username = ?", username).Count(&existing).Error; err != nil {
			return result, fmt.Errorf("查询用户失败: %w", err)
		}
		if existing > 0 {
			entry.Result = ImportRowSkipped
			entry.Reason = "用户名已存在"
			result.Details = append(result.Details, entry)
			result.Skipped++
			continue
		}

		phoneRaw := importCell(row, index, "phone")
		var phone *string
		if phoneRaw != "" {
			normalized, phoneErr := NormalizeMainlandPhoneInput(phoneRaw)
			if phoneErr != nil {
				entry.Result = ImportRowFailed
				entry.Reason = "phone 不是合法的手机号"
				result.Details = append(result.Details, entry)
				result.Failed++
				continue
			}
			phone = &normalized
			if phoneErr = ensureImportPhoneAvailable(db, normalized); phoneErr != nil {
				entry.Result = ImportRowSkipped
				entry.Reason = "手机号已被占用"
				result.Details = append(result.Details, entry)
				result.Skipped++
				continue
			}
		}
		balance, balanceErr := parseImportAmount(importCell(row, index, "balance"))
		if balanceErr != nil {
			entry.Result = ImportRowFailed
			entry.Reason = "balance 必须是数字"
			result.Details = append(result.Details, entry)
			result.Failed++
			continue
		}
		giftBalance, giftErr := parseImportAmount(importCell(row, index, "gift_balance"))
		if giftErr != nil {
			entry.Result = ImportRowFailed
			entry.Reason = "gift_balance 必须是数字"
			result.Details = append(result.Details, entry)
			result.Failed++
			continue
		}
		hashed, hashErr := util.HashPassword(password)
		if hashErr != nil {
			entry.Result = ImportRowFailed
			entry.Reason = "密码处理失败"
			result.Details = append(result.Details, entry)
			result.Failed++
			continue
		}
		createdAt := now()
		user := entity.User{
			Username:       username,
			Password:       hashed,
			Role:           "user",
			Status:         "enabled",
			Balance:        balance,
			GiftBalance:    giftBalance,
			RegisterSource: "import",
			Phone:          phone,
			CreatedAt:      createdAt,
		}
		if phone != nil {
			user.PhoneBoundAt = &createdAt
		}
		if err := db.Create(&user).Error; err != nil {
			if isDuplicateBlacklistError(err) {
				entry.Result = ImportRowSkipped
				entry.Reason = "用户名或手机号已存在"
				result.Details = append(result.Details, entry)
				result.Skipped++
				continue
			}
			entry.Result = ImportRowFailed
			entry.Reason = err.Error()
			result.Details = append(result.Details, entry)
			result.Failed++
			continue
		}
		entry.Result = ImportRowCreated
		result.Details = append(result.Details, entry)
		result.Succeeded++

		// 导入用户也要写 active 绑定记录（T-25 采集侧的第 4 条入口）。
		//
		// 为什么必须补：导入是**唯一绕过** CreateUserWithNewUserBonusInTx 的建号
		// 路径，它直接 db.Create 却带着 Phone + PhoneBoundAt。不补的话，导入的
		// 用户在 user_binding_records 里没有任何行 —— 而 R14 binding_churn、
		// 44 号追缴的 history_phone_no_bonus、19 号四个开关的 Current* 分支
		// 全都依赖这张表。
		//
		// 失败只记日志、不回滚整行导入：绑定记录是**旁路审计**，写不进去不该让
		// 「用户已建成」被撤销（那会让导入结果与实际数据不一致）。这与注册路径
		// 的取舍不同 —— 那里在事务内、失败应整体回滚；这里是逐行导入，单行审计
		// 失败不影响主记录的正确性。
		if err := recordActiveBindingsForNewUser(db, &user); err != nil {
			logger.SysWarn(fmt.Sprintf("导入用户 %q 后写绑定记录失败（不影响导入结果）：%v", username, err))
		}
	}
	result.ErrorReport = buildImportErrorReport(result.Details)
	return result, nil
}

// ensureImportPhoneAvailable 检查手机号是否已被占用。
func ensureImportPhoneAvailable(db *gorm.DB, phone string) error {
	var count int64
	if err := db.Model(&entity.User{}).Where("phone = ?", phone).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("手机号已被占用")
	}
	return nil
}

// parseImportAmount 解析金额列（空 = 0）。
func parseImportAmount(value string) (float64, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, nil
	}
	parsed, err := strconv.ParseFloat(trimmed, 64)
	if err != nil {
		return 0, err
	}
	return parsed, nil
}

// IsWeakImportPassword 判断导入的密码是否弱密码（任务书 ④ 的判据）。
//
// 与注册侧的 validatePasswordComplexity 同一口径：至少 8 位，且含大写 / 小写 / 数字 / 特殊字符。
// 刻意**不是**「长度不够就拒绝」——弱密码只是需要二次确认（见 importUserRows 的说明）。
//
// 为什么不直接调 controller 的 validatePasswordComplexity：它在 controller 包，
// service 不能反向依赖（controller → service 是既有方向）。口径必须两边一致，
// 所以这里复刻一次，并在两边注释里互相指路。
func IsWeakImportPassword(password string) bool {
	trimmed := strings.TrimSpace(password)
	if len([]rune(trimmed)) < 8 {
		return true
	}
	hasUpper, hasLower, hasDigit, hasSpecial := false, false, false, false
	for _, ch := range trimmed {
		switch {
		case ch >= 'A' && ch <= 'Z':
			hasUpper = true
		case ch >= 'a' && ch <= 'z':
			hasLower = true
		case ch >= '0' && ch <= '9':
			hasDigit = true
		default:
			hasSpecial = true
		}
	}
	return !(hasUpper && hasLower && hasDigit && hasSpecial)
}

// joinImportRowNumbers 把行号列表拼成「2、5、9」（超过 10 个用「等 N 行」收尾）。
func joinImportRowNumbers(rows []int) string {
	const preview = 10
	parts := make([]string, 0, preview)
	for i, row := range rows {
		if i >= preview {
			break
		}
		parts = append(parts, strconv.Itoa(row))
	}
	text := strings.Join(parts, "、")
	if len(rows) > preview {
		text += fmt.Sprintf(" 等 %d 行", len(rows))
	}
	return text
}

// buildImportErrorReport 生成失败/跳过行的 CSV 报告（111 §7.5 的 errors.csv）。
//
// 只写有问题的行：成功行不需要报告（它们的证据就是库里有了）。
// 带 BOM 以便 Excel 打开中文不乱码。
func buildImportErrorReport(details []ImportRowResult) []byte {
	problem := make([]ImportRowResult, 0, len(details))
	for _, detail := range details {
		if detail.Result == ImportRowFailed || detail.Result == ImportRowSkipped {
			problem = append(problem, detail)
		}
	}
	if len(problem) == 0 {
		return nil
	}
	buffer := &bytes.Buffer{}
	buffer.WriteString("\uFEFF")
	writer := csv.NewWriter(buffer)
	if err := writer.Write([]string{"row", "key", "result", "reason"}); err != nil {
		return nil
	}
	for _, detail := range problem {
		if err := writer.Write([]string{
			strconv.Itoa(detail.Row), detail.Key, detail.Result, detail.Reason,
		}); err != nil {
			return nil
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil
	}
	return buffer.Bytes()
}

// ImportResourceList 返回支持导入的资源标识（升序）。
func ImportResourceList() []string {
	return []string{ImportResourceBlacklist, ImportResourceIPLocations, ImportResourceOCRCache, ImportResourcePhoneLocations, ImportResourceQuestions, ImportResourceSensitiveImages, ImportResourceUsers, ImportResourceWhitelist}
}

// ImportTemplateCSV 返回某资源的导入模板（只有表头，111 §4.1 Q12-h 的「下载模板」按钮）。
//
// 只给表头而不给示例行：示例行会被不加思考地当真数据提交，
// 而表头 + 列名本身就是最好的说明书（列名见 111 §7.4 / §7.5）。
func ImportTemplateCSV(resource string) ([]byte, error) {
	var header []string
	switch strings.ToLower(strings.TrimSpace(resource)) {
	case ImportResourceBlacklist, ImportResourceWhitelist:
		header = managementRuleHeaders()
	case ImportResourceQuestions:
		header = []string{"question", "type", "options", "answer", "course_name", "extra", "source_name"}
	case ImportResourceUsers:
		header = []string{"username", "password", "phone", "level_rank", "balance", "gift_balance", "remark"}
	case ImportResourceIPLocations, ImportResourcePhoneLocations:
		header = managementLocationHeaders()
	case ImportResourceSensitiveImages:
		header = sensitiveImageTransferHeaders()
	case ImportResourceOCRCache:
		header = ocrTransferHeaders()
	default:
		return nil, fmt.Errorf("%w: %q", ErrImportResourceUnsupported, resource)
	}
	buffer := &bytes.Buffer{}
	buffer.WriteString("\uFEFF")
	writer := csv.NewWriter(buffer)
	if err := writer.Write(header); err != nil {
		return nil, err
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// ImportTemplateData serializes a template using the selected format. CSV keeps
// legacy templates for all resources; other formats are supported for questions.
func ImportTemplateData(resource, format string) ([]byte, error) {
	resource = strings.ToLower(strings.TrimSpace(resource))
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "" {
		format = ExportFormatCSV
	}
	if format == ExportFormatCSV {
		return ImportTemplateCSV(resource)
	}
	if resource != ImportResourceQuestions {
		if format == ExportFormatJSON || format == ExportFormatXlsx {
			content, err := ImportTemplateCSV(resource)
			if err != nil {
				return nil, err
			}
			header, err := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(content, []byte("\uFEFF")))).Read()
			if err != nil {
				return nil, err
			}
			columns := make([]ExportColumn, 0, len(header))
			for _, name := range header {
				columns = append(columns, exportColumn(name))
			}
			return serializeExportRows(columns, [][]string{make([]string, len(columns))}, format)
		}
		return nil, fmt.Errorf("%w：当前格式的模板仅支持题库", ErrImportResourceUnsupported)
	}
	switch format {
	case ExportFormatJSON, ExportFormatXlsx, ExportFormatXML, ExportFormatHTML:
	default:
		return nil, fmt.Errorf("不支持的模板格式：%s", format)
	}
	columns := make([]ExportColumn, 0, 7)
	for _, header := range []string{"question", "type", "options", "answer", "course_name", "extra", "source_name"} {
		columns = append(columns, exportColumn(header))
	}
	row := make([]string, len(columns))
	for i, column := range columns {
		if column.Header == "options" || column.Header == "answer" {
			row[i] = "[]"
		}
	}
	return serializeExportRows(columns, [][]string{row}, format)
}
