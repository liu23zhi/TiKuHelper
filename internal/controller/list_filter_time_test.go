package controller

import (
	"testing"
	"time"
)

func TestParseListFilterTimeSupportsDateBoundsAndRFC3339(t *testing.T) {
	start, err := parseListFilterTime("2026-06-20", false)
	if err != nil || start == nil {
		t.Fatalf("parse date-only start: value=%v err=%v", start, err)
	}
	if !start.Equal(time.Date(2026, 6, 20, 0, 0, 0, 0, time.Local)) {
		t.Fatalf("unexpected date-only start: %v", start)
	}

	end, err := parseListFilterTime("2026-06-20", true)
	if err != nil || end == nil {
		t.Fatalf("parse date-only end: value=%v err=%v", end, err)
	}
	if !end.Equal(time.Date(2026, 6, 21, 0, 0, 0, 0, time.Local)) {
		t.Fatalf("date-only end must be exclusive next day, got %v", end)
	}

	rfc3339, err := parseListFilterTime("2026-06-20T12:34:56+08:00", false)
	if err != nil || rfc3339 == nil || rfc3339.Format(time.RFC3339) != "2026-06-20T12:34:56+08:00" {
		t.Fatalf("parse RFC3339: value=%v err=%v", rfc3339, err)
	}

	if value, err := parseListFilterTime("not-a-date", false); err == nil || value != nil {
		t.Fatalf("invalid date must be rejected: value=%v err=%v", value, err)
	}
}

func TestParseListFilterNumbersRejectInvalidValues(t *testing.T) {
	if got, err := parseOptionalPositiveInt32("42"); err != nil || got != 42 {
		t.Fatalf("parse positive integer: value=%d err=%v", got, err)
	}
	if got, err := parseOptionalPositiveInt32("0"); err == nil || got != 0 {
		t.Fatalf("zero user id must be rejected: value=%d err=%v", got, err)
	}
	if got, err := parseOptionalNonNegativeFloat("12.50"); err != nil || got == nil || *got != 12.5 {
		t.Fatalf("parse amount: value=%v err=%v", got, err)
	}
	if got, err := parseOptionalNonNegativeFloat("-1"); err == nil || got != nil {
		t.Fatalf("negative amount must be rejected: value=%v err=%v", got, err)
	}
	for _, input := range []string{"NaN", "Inf", "-Inf"} {
		if got, err := parseOptionalNonNegativeFloat(input); err == nil || got != nil {
			t.Fatalf("non-finite amount must be rejected: input=%q value=%v err=%v", input, got, err)
		}
	}
}

func TestValidateListFilterTimeRangeRequiresStrictOrdering(t *testing.T) {
	from := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	if err := validateListFilterTimeRange(&from, &to, "创建时间"); err != nil {
		t.Fatalf("valid range rejected: %v", err)
	}
	for _, invalidTo := range []*time.Time{&from, func() *time.Time { value := from.Add(-time.Nanosecond); return &value }()} {
		if err := validateListFilterTimeRange(&from, invalidTo, "支付时间"); err == nil || err.Error() != "支付时间范围无效：开始时间必须早于结束时间" {
			t.Fatalf("invalid range must include field-specific error, got %v", err)
		}
	}
	if err := validateListFilterTimeRange(nil, &to, "创建时间"); err != nil {
		t.Fatalf("open-ended range rejected: %v", err)
	}
}
