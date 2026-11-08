package test

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/itihey/tikuAdapter/internal/search"
	"github.com/itihey/tikuAdapter/pkg/model"
)

func TestSearchIcodefClient_SearchAnswer(t *testing.T) {
	req := model.SearchRequest{Question: "LOCAL_FIXTURE_ICODEF"}
	useProviderFixture(t, providerFixture{name: "icodef", scheme: "https", host: "cx.icodef.com", path: "/wyn-nb", method: http.MethodPost, query: url.Values{"v": {"4"}}, form: url.Values{"question": {req.Question}}, headers: map[string]string{"Authorization": syntheticProviderToken, "Content-Type": "application/x-www-form-urlencoded"}, response: `{"code":1,"data":"甲#乙","msg":"synthetic"}`})
	disabled := search.IcodefClient{}
	got, err := disabled.SearchAnswer(req)
	assertProviderAnswers(t, got, err, [][]string{})
	assertNoProviderRequests(t, "icodef disabled original input")
	client := search.IcodefClient{Enable: true, Token: syntheticProviderToken}
	got, err = client.SearchAnswer(req)
	assertProviderAnswers(t, got, err, [][]string{{"甲", "乙"}})
}

func TestSearchWannengClient_SearchAnswer(t *testing.T) {
	req := model.SearchRequest{Question: "LOCAL_FIXTURE_WANNENG", Options: []string{"选项一", "选项二"}, Type: 0}
	useProviderFixture(t, providerFixture{name: "wanneng", scheme: "http", host: "lyck6.cn", path: "/scriptService/api/autoFreeAnswer", method: http.MethodPost, query: url.Values{}, jsonBody: fixtureJSON(t, req), headers: map[string]string{"Content-Type": "application/json"}, response: `{"code":0,"result":{"success":true,"answers":[0]}}`})
	client := search.WannengClient{Enable: true}
	got, err := client.SearchAnswer(req)
	want := make([][]string, 10)
	for i := range want {
		want[i] = []string{"选项一"}
	}
	assertProviderAnswers(t, got, err, want)
}

func TestSearchEnncyClient_SearchAnswer(t *testing.T) {
	req := model.SearchRequest{Question: "LOCAL_FIXTURE_ENNCY"}
	useProviderFixture(t, providerFixture{name: "enncy", scheme: "https", host: "tk.enncy.cn", path: "/query", method: http.MethodGet, query: url.Values{"token": {syntheticProviderToken}, "title": {req.Question}}, response: `{"code":1,"data":{"answer":"甲#乙"}}`})
	emptyToken := search.EnncyClient{Enable: true}
	got, err := emptyToken.SearchAnswer(req)
	assertProviderAnswers(t, got, err, [][]string{})
	assertNoProviderRequests(t, "enncy empty-token original input")
	client := search.EnncyClient{Enable: true, Token: syntheticProviderToken}
	got, err = client.SearchAnswer(req)
	assertProviderAnswers(t, got, err, [][]string{{"甲", "乙"}})
}

func TestBuguake_SearchAnswer(t *testing.T) {
	req := model.SearchRequest{Question: "LOCAL_FIXTURE_BUGUAKE"}
	useProviderFixture(t, providerFixture{name: "buguake", scheme: "https", host: "easylearn.baidu.com", path: "/edu-web-go/bgk/searchlist", method: http.MethodGet, query: url.Values{"query": {req.Question}, "rn": {"10"}, "pn": {"0"}}, response: syntheticBuguakeEnvelope(t, req.Question)})
	client := search.BuguakeClient{Enable: true}
	got, err := client.SearchAnswer(req)
	assertProviderAnswers(t, got, err, [][]string{{"黄州"}})
}

func TestAidian_SearchAnswer(t *testing.T) {
	req := model.SearchRequest{Question: "LOCAL_FIXTURE_AIDIAN", Type: 0}
	useProviderFixture(t, providerFixture{name: "aidian", scheme: "http", host: "new.api.51aidian.com", path: "/publics/newapi/freedirect", method: http.MethodPost, query: url.Values{}, jsonBody: fixtureJSON(t, map[string]string{"question": req.Question, "token": ""}), response: `{"code":200,"qlist":[{"question":"LOCAL_FIXTURE_AIDIAN","options":["A. 正确","B. 错误"],"answer":["A"]}]}`})
	// Preserve and expose the current inverted Enable behavior, not a product fix.
	original := search.AidianClient{Enable: true}
	got, err := original.SearchAnswer(req)
	assertProviderAnswers(t, got, err, [][]string{})
	assertNoProviderRequests(t, "aidian Enable=true current original branch")
	client := search.AidianClient{Enable: false}
	got, err = client.SearchAnswer(req)
	assertProviderAnswers(t, got, err, [][]string{{"正确"}})
}

func TestSearchLemonClient_SearchAnswer(t *testing.T) {
	req := model.SearchRequest{Question: "LOCAL_FIXTURE_LEMON"}
	useProviderFixture(t, providerFixture{name: "lemon", scheme: "https", host: "api.lemtk.xyz", path: "/api/v1/mcx", method: http.MethodPost, query: url.Values{}, jsonBody: fixtureJSON(t, map[string]string{"v": "1.0", "question": req.Question, "uid": "703382225"}), headers: map[string]string{"Authorization": "Bearer " + syntheticProviderToken, "Content-Type": "application/json"}, response: `{"code":1000,"data":{"answer":"甲#乙"}}`})
	client := search.LemonClient{Enable: true, Token: syntheticProviderToken}
	got, err := client.SearchAnswer(req)
	assertProviderAnswers(t, got, err, [][]string{{"甲", "乙"}})
}

func TestSearchTikuhaiClient_SearchAnswer(t *testing.T) {
	req := model.SearchRequest{Question: "LOCAL_FIXTURE_TIKUHAI", Options: []string{"甲", "乙"}, Type: 0}
	useProviderFixture(t, providerFixture{name: "tikuhai", scheme: "https", host: "api.tikuhai.com", path: "/search", method: http.MethodPost, query: url.Values{}, jsonBody: fixtureJSON(t, map[string]any{"question": req.Question, "options": req.Options, "type": req.Type, "key": syntheticProviderToken, "questionData": ""}), headers: map[string]string{"Content-Type": "application/json", "User-Agent": "tikuhaiAdapter/0.1.0", "v": "0.1.0"}, response: `{"code":200,"data":{"answer":["甲"],"num":1,"usenum":0}}`})
	client := search.TikuhaiClient{Enable: true, Token: syntheticProviderToken}
	got, err := client.SearchAnswer(req)
	assertProviderAnswers(t, got, err, [][]string{{"甲"}})
}
