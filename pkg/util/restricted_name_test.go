package util

import (
	"strings"
	"testing"
)

func TestValidateRestrictedName(t *testing.T) {
	tests := []struct {
		name      string
		value     string
		maxLength int
		wantError bool
	}{
		{name: "allowed characters", value: "中文A9_@", maxLength: UsernameMaxLength},
		{name: "exact length", value: strings.Repeat("中", AccessTokenNameMaxLength), maxLength: AccessTokenNameMaxLength},
		{name: "empty", value: "", maxLength: UsernameMaxLength, wantError: true},
		{name: "url", value: "https://example.com", maxLength: UsernameMaxLength, wantError: true},
		{name: "punctuation", value: "bad-name", maxLength: UsernameMaxLength, wantError: true},
		{name: "overlong by rune count", value: strings.Repeat("中", AccessTokenNameMaxLength+1), maxLength: AccessTokenNameMaxLength, wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRestrictedName(tt.value, tt.maxLength, "名称")
			if (err != nil) != tt.wantError {
				t.Fatalf("ValidateRestrictedName() error = %v, wantError %v", err, tt.wantError)
			}
		})
	}
}
