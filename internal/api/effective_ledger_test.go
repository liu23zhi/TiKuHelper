package api

import "testing"

func TestEffectiveLedgerRoutes(t *testing.T) {
	eng := ginNewEngineForDeviceRoutes()
	SetAPIRouter(eng)
	got := map[string]bool{}
	for _, r := range eng.Routes() {
		got[r.Method+" "+r.Path] = true
	}
	for _, route := range []string{"GET /api/admin/users/:id/effective-ledger", "POST /api/admin/users/:id/effective-ledger", "POST /api/admin/effective-ledger/rebuild"} {
		if !got[route] {
			t.Fatal(route)
		}
	}
}
