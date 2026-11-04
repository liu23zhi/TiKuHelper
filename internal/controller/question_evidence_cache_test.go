package controller

import (
	"github.com/itihey/tikuAdapter/pkg/model"
	"testing"
)

func TestQuestionImageEvidenceSeparatesSearchAndFallbackCaches(t *testing.T) {
	req := model.SearchRequest{Question: "图片题", Type: -1}
	searchA, _, ok := buildSearchResultCacheKey(resolvedSearchRequestContext{UserID: 1}, req, "ai")
	if !ok {
		t.Fatal("search cache key unavailable")
	}
	fallbackA, _, ok := buildFallbackResultCacheKey(1, req)
	flightA := buildFallbackFlightKey(1, req)
	if !ok {
		t.Fatal("fallback cache key unavailable")
	}
	req.QuestionEvidence = &model.QuestionEvidence{StemImages: []model.QuestionImageEvidence{{URL: "https://example.com/a.png", Alt: "第一幅图", Index: 0}}}
	searchB, _, _ := buildSearchResultCacheKey(resolvedSearchRequestContext{UserID: 1}, req, "ai")
	fallbackB, _, _ := buildFallbackResultCacheKey(1, req)
	flightB := buildFallbackFlightKey(1, req)
	req.QuestionEvidence.StemImages[0].Alt = "第二幅图"
	searchC, _, _ := buildSearchResultCacheKey(resolvedSearchRequestContext{UserID: 1}, req, "ai")
	fallbackC, _, _ := buildFallbackResultCacheKey(1, req)
	flightC := buildFallbackFlightKey(1, req)
	if searchA == searchB || searchB == searchC || fallbackA == fallbackB || fallbackB == fallbackC || flightA == flightB || flightB == flightC {
		t.Fatal("different image context must not share answers")
	}
}
