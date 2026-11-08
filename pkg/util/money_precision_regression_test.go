package util

import (
	"math"
	"testing"
)

func TestMoneyPrecisionRegressionHalfMicroBoundaries(t *testing.T) {
	if MoneyPrecision != 6 || RechargePrecision != 2 {
		t.Fatalf("money/recharge precision = %d/%d, want 6/2", MoneyPrecision, RechargePrecision)
	}

	const halfMicro = 0.0000005
	cases := []struct {
		name   string
		amount float64
		want   float64
	}{
		{"zero", 0, 0},
		{"positive_below_half_micro", math.Nextafter(halfMicro, 0), 0},
		{"positive_half_micro", halfMicro, 0.000001},
		{"positive_above_half_micro", math.Nextafter(halfMicro, 1), 0.000001},
		{"negative_below_half_magnitude", math.Nextafter(-halfMicro, 0), 0},
		{"negative_half_micro", -halfMicro, -0.000001},
		{"negative_above_half_magnitude", math.Nextafter(-halfMicro, -1), -0.000001},
		{"one_micro", 0.000001, 0.000001},
		{"negative_one_micro", -0.000001, -0.000001},
		{"sub_cent_charge", 0.000062, 0.000062},
		{"negative_sub_cent_amount", -0.000062, -0.000062},
		{"below_cent_carry", 0.0099994, 0.009999},
		{"above_cent_carry", 0.0099996, 0.01},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RoundMoney(tc.amount); got != tc.want {
				t.Fatalf("RoundMoney(%.17g) = %.17g, want %.17g", tc.amount, got, tc.want)
			}
		})
	}
}

func TestMoneyPrecisionRegressionMicroUnitGrid(t *testing.T) {
	// Integer micro-units provide an oracle independent of the rounding helper.
	// Quarter offsets avoid ambiguous decimal ties except the explicit half-micro
	// cases above. This is bounded fixture coverage, not an all-float64 claim.
	offsets := []struct {
		quarters     int64
		roundedUnits int64
	}{{-3, -1}, {-1, 0}, {0, 0}, {1, 0}, {3, 1}}
	checked := 0
	check := func(units int64) {
		for _, offset := range offsets {
			amount := (float64(units) + float64(offset.quarters)/4) / 1_000_000
			want := float64(units+offset.roundedUnits) / 1_000_000
			got := RoundMoney(amount)
			if got != want {
				t.Fatalf("units=%d quarters=%d: got %.17g, want %.17g", units, offset.quarters, got, want)
			}
			if again := RoundMoney(got); again != got {
				t.Fatalf("rounding not idempotent: %.17g -> %.17g", got, again)
			}
			if negative := RoundMoney(-amount); negative != -got {
				t.Fatalf("rounding not sign-symmetric for %.17g: got %.17g / %.17g", amount, got, negative)
			}
			if math.Abs(got-amount) > 0.000000500001 {
				t.Fatalf("rounding moved %.17g by more than half a micro-unit: %.17g", amount, got)
			}
			checked++
		}
	}
	for units := int64(-2048); units <= 2048; units++ {
		check(units)
	}
	for _, units := range []int64{-1_234_567_890, -1_234_567, -1_000_001, -1_000_000, 1_000_000, 1_000_001, 1_234_567, 1_234_567_890} {
		check(units)
	}
	t.Logf("verified %d bounded micro-unit inputs", checked)
}

func TestMoneyPrecisionRegressionDecimalClamp(t *testing.T) {
	cases := []struct {
		name     string
		decimals int
		want     float64
	}{
		{"negative_uses_integer_precision", -4, 12},
		{"zero_uses_integer_precision", 0, 12},
		{"recharge_cents", 2, 12.35},
		{"money_micro_units", 6, 12.345679},
		{"upper_boundary", 8, 12.34567891},
		{"above_upper_boundary", 9, 12.34567891},
		{"large_decimals_clamped", 64, 12.34567891},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, sign := range []float64{-1, 1} {
				amount, want := sign*12.345678914, sign*tc.want
				if got := RoundMoneyToDecimals(amount, tc.decimals); got != want {
					t.Fatalf("RoundMoneyToDecimals(%.17g, %d) = %.17g, want %.17g", amount, tc.decimals, got, want)
				}
			}
		})
	}
}

func TestMoneyPrecisionRegressionRechargeDecimalValidation(t *testing.T) {
	oneTenth, twoTenths := 0.1, 0.2
	cases := []struct {
		name     string
		amount   float64
		decimals int
		wantMore bool
	}{
		{"whole_amount", 12, RechargePrecision, false},
		{"cent_amount", 12.34, RechargePrecision, false},
		{"negative_cent_utility_input", -12.34, RechargePrecision, false},
		{"float_sum_tail", oneTenth + twoTenths, RechargePrecision, false},
		{"one_ulp_above_cent", math.Nextafter(12.34, 13), RechargePrecision, false},
		{"one_ulp_below_cent", math.Nextafter(12.34, 12), RechargePrecision, false},
		{"third_decimal_rejected", 0.001, RechargePrecision, true},
		{"one_micro_rejected_as_recharge", 0.000001, RechargePrecision, true},
		{"cent_plus_one_micro", 12.340001, RechargePrecision, true},
		{"negative_cent_plus_one_micro", -12.340001, RechargePrecision, true},
		// The helper's existing 1e-9 tolerance is in scaled units, not yuan.
		{"inside_scaled_tolerance", 0.01 + 0.000000000005, RechargePrecision, false},
		{"outside_scaled_tolerance", 0.01 + 0.00000000002, RechargePrecision, true},
		{"negative_inside_scaled_tolerance", -(0.01 + 0.000000000005), RechargePrecision, false},
		{"negative_outside_scaled_tolerance", -(0.01 + 0.00000000002), RechargePrecision, true},
		{"micro_allowed_as_money", 0.000001, MoneyPrecision, false},
		{"negative_micro_allowed_as_money", -0.000001, MoneyPrecision, false},
		{"seventh_decimal_rejected", 0.0000001, MoneyPrecision, true},
		{"negative_seventh_decimal_rejected", -0.0000001, MoneyPrecision, true},
		{"zero_decimal_integer", 12, 0, false},
		{"zero_decimal_fraction", 12.25, 0, true},
		{"negative_decimal_floor_integer", 12, -4, false},
		{"negative_decimal_floor_fraction", 12.25, -4, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HasMoreDecimalsThan(tc.amount, tc.decimals); got != tc.wantMore {
				t.Fatalf("HasMoreDecimalsThan(%.17g, %d) = %t, want %t", tc.amount, tc.decimals, got, tc.wantMore)
			}
		})
	}
}
