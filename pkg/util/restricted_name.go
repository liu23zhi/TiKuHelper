package util

import (
	"fmt"
	"unicode"
	"unicode/utf8"
)

const (
	// These limits mirror users.username varchar(255) and access_tokens.name varchar(128).
	UsernameMaxLength        = 255
	AccessTokenNameMaxLength = 128
)

func ValidateRestrictedName(value string, maxLength int, label string) error {
	if value == "" {
		return fmt.Errorf("%s不能为空", label)
	}
	if utf8.RuneCountInString(value) > maxLength {
		return fmt.Errorf("%s长度不能超过%d个字符", label, maxLength)
	}
	for _, character := range value {
		if character == '_' || character == '@' ||
			character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			unicode.Is(unicode.Han, character) {
			continue
		}
		return fmt.Errorf("%s只能使用中文、英文、数字、下划线和@", label)
	}
	return nil
}
