package main

import (
	"reflect"
	"testing"
)

func TestResolveTrustedProxiesTrustsAllWhenEmpty(t *testing.T) {
	got := resolveTrustedProxies("")
	expected := []string{"0.0.0.0/0", "::/0"}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("expected trust-all proxies %v, got %v", expected, got)
	}
}

func TestResolveTrustedProxiesUsesConfiguredValues(t *testing.T) {
	got := resolveTrustedProxies(" 10.0.0.10, 172.18.0.0/16 ")
	expected := []string{"10.0.0.10", "172.18.0.0/16"}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("expected configured proxies %v, got %v", expected, got)
	}
}
