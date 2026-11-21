package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDetectImportFormatSupportsXMLAndHTML(t *testing.T) {
	for name, want := range map[string]string{
		"questions.xml":  ImportFormatXML,
		"questions.html": ImportFormatHTML,
		"questions.htm":  ImportFormatHTML,
		"questions.txt":  ImportFormatTXT,
	} {
		got, err := DetectImportFormat(name, []byte("<questions/>"))
		if err != nil || got != want {
			t.Errorf("DetectImportFormat(%q) = %q, %v; want %q", name, got, err, want)
		}
	}
}

func TestParseImportTXTSupportsTabularRows(t *testing.T) {
	content := []byte("question\ttype\toptions\tanswer\n题目甲\t0\t[]\t[A]\n")
	header, rows, err := ParseImportRows(ImportFormatTXT, content)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || importCell(rows[0], importColumnIndex(header), "question") != "题目甲" || importCell(rows[0], importColumnIndex(header), "type") != "0" {
		t.Fatalf("parsed rows = %#v, header = %#v", rows, header)
	}
}

func TestQuestionImportTemplatesMatchSelectedFormat(t *testing.T) {
	for _, format := range []string{ExportFormatCSV, ExportFormatJSON, ExportFormatXlsx, ExportFormatXML, ExportFormatHTML} {
		t.Run(format, func(t *testing.T) {
			content, err := ImportTemplateData(ImportResourceQuestions, format)
			if err != nil || len(content) == 0 {
				t.Fatalf("template: bytes=%d err=%v", len(content), err)
			}
			switch format {
			case ExportFormatJSON:
				var items []map[string]any
				if err := json.Unmarshal(content, &items); err != nil || len(items) != 1 || items[0]["question"] != "" {
					t.Fatalf("JSON template=%s err=%v", content, err)
				}
			}
			header, rows, err := ParseImportRows(format, content)
			_, hasQuestion := importColumnIndex(header)["question"]
			if format == ExportFormatCSV {
				if !hasQuestion || len(header) == 0 || err == nil {
					// CSV templates intentionally contain only a header and therefore
					// normalizeImportTable reports ErrImportFileEmpty.
					t.Fatalf("CSV template header=%v rows=%d err=%v", header, len(rows), err)
				}
				return
			}
			if err != nil || len(rows) != 1 || !hasQuestion {
				t.Fatalf("%s template cannot be parsed as a question import: header=%v rows=%d err=%v", format, header, len(rows), err)
			}
		})
	}
}

func TestParseImportXMLSupportsQuestionRows(t *testing.T) {
	content := []byte(`<?xml version="1.0"?><questions><question><question>题目甲</question><type>0</type><options>["A","B"]</options><answer>["A"]</answer><course_name>数学</course_name></question><question><question>题目乙</question><type>1</type><options>[]</options><answer>["答案"]</answer></question></questions>`)
	header, rows, err := ParseImportRows(ImportFormatXML, content)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || importCell(rows[0], importColumnIndex(header), "question") != "题目甲" || importCell(rows[1], importColumnIndex(header), "question") != "题目乙" {
		t.Fatalf("parsed rows = %#v, header = %#v", rows, header)
	}
}

func TestParseImportHTMLSupportsQuestionRows(t *testing.T) {
	content := []byte(`<table><thead><tr><th>question</th><th>type</th><th>options</th><th>answer</th></tr></thead><tbody><tr><td>题目&amp;甲</td><td>0</td><td>[]</td><td>[&quot;A&quot;]</td></tr></tbody></table>`)
	header, rows, err := ParseImportRows(ImportFormatHTML, content)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || importCell(rows[0], importColumnIndex(header), "question") != "题目&甲" {
		t.Fatalf("parsed rows = %#v, header = %#v", rows, header)
	}
}

func TestParseRealBIFFXlsQuestionFixture(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("testdata", "legacy.xls"))
	if err != nil {
		t.Fatal(err)
	}
	header, rows, err := ParseImportRows(ImportFormatXls, fixture)
	if err != nil {
		t.Fatal(err)
	}
	if len(header) == 0 || len(rows) == 0 {
		t.Fatalf("header=%v rows=%d", header, len(rows))
	}
}

func TestExportQuestionsSupportsJSONXMLHTMLAndXLSX(t *testing.T) {
	db := newImportTestDB(t)
	if _, err := ImportDataWithDB(db, ImportDataOptions{Resource: ImportResourceQuestions, Format: ImportFormatJSON, Content: []byte(`[{"question":"导出题目","type":0,"options":["A"],"answer":["A"],"course_name":"数学"}]`)}); err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{ExportFormatJSON, ExportFormatXML, ExportFormatHTML, ExportFormatXlsx} {
		t.Run(format, func(t *testing.T) {
			result, err := ExportDataWithDB(db, ExportDataOptions{Resource: ExportResourceQuestions, Format: format})
			if err != nil {
				t.Fatal(err)
			}
			if result.Rows != 1 || len(result.Content) == 0 {
				t.Fatalf("result = %#v", result)
			}
			if format == ExportFormatJSON {
				var items []map[string]any
				if err := json.Unmarshal(result.Content, &items); err != nil || len(items) != 1 || items[0]["question"] != "导出题目" {
					t.Fatalf("JSON round trip: items=%#v err=%v", items, err)
				}
			}
			if format == ExportFormatXML && !strings.Contains(string(result.Content), "导出题目") {
				t.Fatalf("XML does not include question: %s", result.Content)
			}
			if format == ExportFormatHTML && !strings.Contains(string(result.Content), "导出题目") {
				t.Fatalf("HTML does not include question: %s", result.Content)
			}
		})
	}
}

func TestExportQuestionIDsRestrictsOutput(t *testing.T) {
	db := newImportTestDB(t)
	if _, err := ImportDataWithDB(db, ImportDataOptions{Resource: ImportResourceQuestions, Format: ImportFormatJSON, Content: []byte(`[{"question":"保留题","type":0,"options":["A","B"],"answer":["B"],"course_name":"合成课程","extra":"附注","source_name":"合成来源"},{"question":"未选题","options":[],"answer":[]}]`)}); err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		ID       int64
		Question string
		Hash     string
	}
	if err := db.Table("tiku").Select("id, question, hash").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	var keepID int64
	var keepHash string
	for _, row := range rows {
		if row.Question == "保留题" {
			keepID = row.ID
			keepHash = row.Hash
		}
	}
	result, err := ExportDataWithDB(db, ExportDataOptions{Resource: ExportResourceQuestions, Format: ExportFormatJSON, QuestionIDs: []int64{keepID}})
	if err != nil {
		t.Fatal(err)
	}
	var items []map[string]any
	if err := json.Unmarshal(result.Content, &items); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"id": float64(keepID), "question": "保留题", "type": float64(0), "options": []any{"A", "B"}, "answer": []any{"B"}, "course_name": "合成课程", "extra": "附注", "source_name": "合成来源", "hash": keepHash}
	if keepHash == "" || len(items) != 1 || !reflect.DeepEqual(items[0], want) {
		t.Fatalf("items = %#v", items)
	}
}
