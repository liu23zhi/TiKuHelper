package controller

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// validateListFilterTimeRange requires an explicitly supplied range to have a
// strictly earlier start than its exclusive end.
func validateListFilterTimeRange(from, to *time.Time, label string) error {
	if from == nil || to == nil || from.Before(*to) {
		return nil
	}
	label = strings.TrimSpace(label)
	if label == "" {
		label = "时间"
	}
	return fmt.Errorf("%s范围无效：开始时间必须早于结束时间", label)
}

// parseListFilterTime parses the date formats used by admin list filters.
// A date-only upper bound is exclusive at the following local midnight.
func parseListFilterTime(value string, endOfDay bool) (*time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		parsed, err = time.ParseInLocation("2006-01-02", value, time.Local)
		if err == nil && endOfDay {
			parsed = parsed.AddDate(0, 0, 1)
		}
	}
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func parseOptionalPositiveInt32(value string) (int32, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 32)
	if err != nil || parsed <= 0 {
		if err == nil {
			err = strconv.ErrSyntax
		}
		return 0, err
	}
	return int32(parsed), nil
}

func parseOptionalNonNegativeFloat(value string) (*float64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || parsed < 0 || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		if err == nil {
			err = strconv.ErrSyntax
		}
		return nil, err
	}
	return &parsed, nil
}
