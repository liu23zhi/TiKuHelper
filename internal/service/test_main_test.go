package service

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	_ = os.Setenv("TIKU_ALLOW_PRIVATE_OUTBOUND", "true")
	os.Exit(m.Run())
}
