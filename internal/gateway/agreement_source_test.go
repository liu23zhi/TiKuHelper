package gateway

import (
	"strings"
	"testing"
)

func TestAgreementForwardedWireUsesOriginalPeer(t *testing.T) {
	config, e := embeddedTemplate("tiku-adapter.conf")
	if e != nil {
		t.Fatal(e)
	}
	inc, e := embeddedTemplate("tiku-proxy.inc")
	if e != nil {
		t.Fatal(e)
	}
	for _, s := range []string{"$realip_remote_addr", "$tiku_trusted_proto:$uri", "by=_tiku_agreement_gateway"} {
		if !strings.Contains(string(config), s) {
			t.Errorf("agreement-specific gateway configuration missing %s", s)
		}
	}
	if !strings.Contains(string(inc), "$tiku_agreement_forwarded;") || !strings.Contains(string(inc), "$tiku_route_xff;") {
		t.Error("route-specific source and provenance do not reach backend")
	}
}
