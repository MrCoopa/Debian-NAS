package cronutil

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// NormalizeLegacySchedule rewrites older 6-field or zero-padded cloud-sync
// expressions into valid standard 5-field cron syntax (min hour dom mon dow).
func NormalizeLegacySchedule(schedule string) string {
	s := strings.TrimSpace(schedule)
	switch s {
	case "0 3 45 * * 2":
		return "45 3 * * 2"
	case "0 2 00 * *":
		return "0 2 * * *"
	case "0 0 00 * *":
		return "0 0 * * *"
	default:
		return s
	}
}

// Validate checks that schedule is a well-formed 5-field cron expression.
func Validate(schedule string) error {
	fields := strings.Fields(strings.TrimSpace(schedule))
	if len(fields) != 5 {
		return fmt.Errorf("invalid schedule '%s': expected 5 cron fields (min hour dom mon dow)", schedule)
	}
	for _, f := range fields {
		for _, r := range f {
			if !(r >= '0' && r <= '9') && r != '*' && r != ',' && r != '-' && r != '/' {
				return fmt.Errorf("invalid character '%c' in cron schedule '%s'", r, schedule)
			}
		}
	}
	return nil
}

// MatchesField reports whether a single cron field expression matches val.
func MatchesField(expr string, val int) bool {
	if expr == "*" {
		return true
	}
	for _, part := range strings.Split(expr, ",") {
		part = strings.TrimSpace(part)
		if part == "*" {
			return true
		}
		if strings.HasPrefix(part, "*/") {
			step, err := strconv.Atoi(strings.TrimPrefix(part, "*/"))
			if err == nil && step > 0 && val%step == 0 {
				return true
			}
			continue
		}
		if strings.Contains(part, "-") {
			bounds := strings.SplitN(part, "-", 2)
			lo, err1 := strconv.Atoi(bounds[0])
			hi, err2 := strconv.Atoi(bounds[1])
			if err1 == nil && err2 == nil && val >= lo && val <= hi {
				return true
			}
			continue
		}
		num, err := strconv.Atoi(part)
		if err == nil && num == val {
			return true
		}
	}
	return false
}

// Matches reports whether tm matches the 5-field cron schedule.
func Matches(schedule string, tm time.Time) bool {
	fields := strings.Fields(NormalizeLegacySchedule(schedule))
	if len(fields) != 5 {
		return false
	}
	dow := int(tm.Weekday())
	dowMatch := MatchesField(fields[4], dow) || (dow == 0 && MatchesField(fields[4], 7))
	return MatchesField(fields[0], tm.Minute()) &&
		MatchesField(fields[1], tm.Hour()) &&
		MatchesField(fields[2], tm.Day()) &&
		MatchesField(fields[3], int(tm.Month())) &&
		dowMatch
}

// NextRun returns the next time after from that satisfies schedule, scanning up to 32 days.
func NextRun(schedule string, from time.Time) time.Time {
	fields := strings.Fields(NormalizeLegacySchedule(schedule))
	if len(fields) != 5 {
		return time.Time{}
	}
	candidate := from.Truncate(time.Minute).Add(time.Minute)
	// 32 days * 24h * 60m = 46080 minutes covers daily, weekly, and monthly schedules.
	for i := 0; i < 46080; i++ {
		if Matches(schedule, candidate) {
			return candidate
		}
		candidate = candidate.Add(time.Minute)
	}
	return time.Time{}
}

// DowName converts a cron day-of-week value into its English weekday name.
func DowName(dow string) string {
	switch strings.TrimSpace(dow) {
	case "0", "7":
		return "Sunday"
	case "1":
		return "Monday"
	case "2":
		return "Tuesday"
	case "3":
		return "Wednesday"
	case "4":
		return "Thursday"
	case "5":
		return "Friday"
	case "6":
		return "Saturday"
	default:
		return "day " + dow
	}
}

// FormatFrequency formats a cron schedule into a human-readable frequency summary.
func FormatFrequency(schedule, fallbackDaily string) string {
	norm := NormalizeLegacySchedule(schedule)
	if norm == "" {
		return fallbackDaily
	}
	parts := strings.Fields(norm)
	if len(parts) != 5 {
		return schedule
	}
	minStr, hourStr, domStr, monStr, dowStr := parts[0], parts[1], parts[2], parts[3], parts[4]
	if minStr == "*/15" && hourStr == "*" {
		return "Every 15 minutes"
	}
	if minStr == "*/30" && hourStr == "*" {
		return "Every 30 minutes"
	}
	if minStr == "0" && hourStr == "*" && domStr == "*" && monStr == "*" && dowStr == "*" {
		return "Every hour (at :00)"
	}
	h, hErr := strconv.Atoi(hourStr)
	m, mErr := strconv.Atoi(minStr)
	if hErr == nil && mErr == nil {
		period := "AM"
		h12 := h
		if h == 0 {
			h12 = 12
		} else if h == 12 {
			period = "PM"
		} else if h > 12 {
			h12 = h - 12
			period = "PM"
		}
		timeLabel := fmt.Sprintf("At %02d:%02d (%d:%02d %s)", h, m, h12, m, period)
		if h < 10 {
			timeLabel = fmt.Sprintf("At %02d:%02d (%02d:%02d %s)", h, m, h12, m, period)
		}
		if domStr == "*" && monStr == "*" && dowStr == "*" {
			return timeLabel + ", every day,..."
		}
		if domStr == "*" && monStr == "*" && dowStr != "*" {
			return timeLabel + ", only on " + DowName(dowStr)
		}
		if domStr != "*" && monStr == "*" {
			return fmt.Sprintf("%s, on day %s of month", timeLabel, domStr)
		}
	}
	return norm
}

// FormatRelativeFuture formats a future duration as a short relative phrase.
func FormatRelativeFuture(d time.Duration) string {
	if d <= 0 {
		return "due now"
	}
	if d < time.Minute {
		return "in less than a minute"
	}
	mins := int(d.Minutes() + 0.5)
	if mins < 60 {
		if mins == 1 {
			return "in 1 minute"
		}
		return fmt.Sprintf("in %d minutes", mins)
	}
	hours := int(d.Hours() + 0.5)
	if hours < 24 {
		if hours == 1 {
			return "in about 1 hour"
		}
		return fmt.Sprintf("in about %d hours", hours)
	}
	days := int(d.Hours()/24 + 0.5)
	if days == 1 {
		return "in 1 day"
	}
	return fmt.Sprintf("in %d days", days)
}

// FormatRelativePast formats an elapsed duration as a short relative phrase.
func FormatRelativePast(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return "just now"
	}
	mins := int(d.Minutes() + 0.5)
	if mins < 60 {
		if mins == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", mins)
	}
	hours := int(d.Hours() + 0.5)
	if hours < 24 {
		if hours == 1 {
			return "about 1 hour ago"
		}
		return fmt.Sprintf("about %d hours ago", hours)
	}
	days := int(d.Hours()/24 + 0.5)
	if days == 1 {
		return "1 day ago"
	}
	return fmt.Sprintf("%d days ago", days)
}
