package model

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestSearchRequestContextIsNotClientData(t *testing.T) {
	var request SearchRequest
	if err := json.Unmarshal([]byte(`{"question":"question","RequestContext":{"deadline":"attacker"}}`), &request); err != nil {
		t.Fatal(err)
	}
	if request.RequestContext != nil || request.Question != "question" {
		t.Fatalf("client input changed the request lifecycle: %+v", request)
	}
	request.RequestContext = context.Background()
	encoded, err := json.Marshal(request)
	if err != nil || strings.Contains(strings.ToLower(string(encoded)), "context") {
		t.Fatalf("server context leaked into serialized request: %s err=%v", encoded, err)
	}
}
