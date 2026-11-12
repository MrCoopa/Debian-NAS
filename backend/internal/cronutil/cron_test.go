package cronutil

import (
	"testing"
	"time"
)

func TestCronUtil_NormalizeValidateMatchAndNextRun(t *testing.T) {
	if got := NormalizeLegacySchedule("0 3 45 * * 2"); got != "45 3 * * 2" {
		t.Fatalf("NormalizeLegacySchedule(weekly) = %q, want 45 3 * * 2", got)
	}
	if got := NormalizeLegacySchedule("0 2 00 * *"); got != "0 2 * * *" {
		t.Fatalf("NormalizeLegacySchedule(daily 2am) = %q, want 0 2 * * *", got)
	}
	if got := NormalizeLegacySchedule("0 0 00 * *"); got != "0 0 * * *" {
		t.Fatalf("NormalizeLegacySchedule(midnight) = %q, want 0 0 * * *", got)
	}

	if err := Validate("*/15 8-18 * * 1-5"); err != nil {
		t.Fatalf("expected valid 5-field cron, got error: %v", err)
	}
	if err := Validate("0 3 45 * * 2"); err == nil {
		t.Fatalf("expected 6-field unnormalized cron to fail Validate")
	}

	// 2026-11-10 is a Tuesday (weekday=2)
	tue := time.Date(2026, time.November, 10, 3, 45, 0, 0, time.UTC)
	if !Matches("45 3 * * 2", tue) {
		t.Fatalf("expected Tuesday 03:45 to match '45 3 * * 2'")
	}
	if !Matches("*/15 * * * *", tue) {
		t.Fatalf("expected :45 to match '*/15 * * * *'")
	}
	if Matches("0 * * * *", tue) {
		t.Fatalf("did not expect :45 to match hourly '0 * * * *'")
	}

	// Monthly cron from Nov 2 should find Dec 1 within 32 days
	nov2 := time.Date(2026, time.November, 2, 10, 0, 0, 0, time.UTC)
	nextMonthly := NextRun("0 0 1 * *", nov2)
	if nextMonthly.IsZero() || nextMonthly.Month() != time.December || nextMonthly.Day() != 1 {
		t.Fatalf("expected Dec 1 00:00, got %v", nextMonthly)
	}

	if f := FormatFrequency("45 3 * * 2", "fallback"); f != "At 03:45 (03:45 AM), only on Tuesday" {
		t.Fatalf("FormatFrequency = %q", f)
	}
	if f := FormatFrequency("0 * * * *", "fallback"); f != "Every hour (at :00)" {
		t.Fatalf("FormatFrequency hourly = %q", f)
	}
}

