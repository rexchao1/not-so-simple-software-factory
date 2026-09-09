package controlplane

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
)

const cronSearchYears = 10

type cronField struct {
	allowed  []bool
	wildcard bool
}

type cronSchedule struct {
	minute     cronField
	hour       cronField
	dayOfMonth cronField
	month      cronField
	dayOfWeek  cronField
	location   *time.Location
}

var cronMonths = map[string]int{
	"JAN": 1, "FEB": 2, "MAR": 3, "APR": 4, "MAY": 5, "JUN": 6,
	"JUL": 7, "AUG": 8, "SEP": 9, "OCT": 10, "NOV": 11, "DEC": 12,
}

var cronWeekdays = map[string]int{
	"SUN": 0, "MON": 1, "TUE": 2, "WED": 3, "THU": 4, "FRI": 5, "SAT": 6,
}

func parseCronSchedule(expression, timezone string) (cronSchedule, string, string, error) {
	fields := strings.Fields(expression)
	if len(fields) != 5 {
		return cronSchedule{}, "", "", fmt.Errorf("cron must contain exactly five fields")
	}
	upperExpression := strings.ToUpper(expression)
	if strings.Contains(upperExpression, "CRON_TZ") {
		return cronSchedule{}, "", "", fmt.Errorf("timezone must be supplied separately")
	}
	syntaxOnly := upperExpression
	for name := range cronMonths {
		syntaxOnly = strings.ReplaceAll(syntaxOnly, name, "")
	}
	for name := range cronWeekdays {
		syntaxOnly = strings.ReplaceAll(syntaxOnly, name, "")
	}
	for _, rejected := range []string{"@", "?", "L", "W", "#"} {
		if strings.Contains(syntaxOnly, rejected) {
			return cronSchedule{}, "", "", fmt.Errorf("cron contains unsupported syntax %q", rejected)
		}
	}
	canonicalTimezone := strings.TrimSpace(timezone)
	if canonicalTimezone == "" || canonicalTimezone == "Local" {
		return cronSchedule{}, "", "", fmt.Errorf("timezone must be a valid IANA timezone")
	}
	location, err := time.LoadLocation(canonicalTimezone)
	if err != nil {
		return cronSchedule{}, "", "", fmt.Errorf("timezone must be a valid IANA timezone: %w", err)
	}
	minute, err := parseCronField(fields[0], 0, 59, nil, false)
	if err != nil {
		return cronSchedule{}, "", "", fmt.Errorf("minute field: %w", err)
	}
	hour, err := parseCronField(fields[1], 0, 23, nil, false)
	if err != nil {
		return cronSchedule{}, "", "", fmt.Errorf("hour field: %w", err)
	}
	dayOfMonth, err := parseCronField(fields[2], 1, 31, nil, false)
	if err != nil {
		return cronSchedule{}, "", "", fmt.Errorf("day-of-month field: %w", err)
	}
	month, err := parseCronField(fields[3], 1, 12, cronMonths, false)
	if err != nil {
		return cronSchedule{}, "", "", fmt.Errorf("month field: %w", err)
	}
	dayOfWeek, err := parseCronField(fields[4], 0, 7, cronWeekdays, true)
	if err != nil {
		return cronSchedule{}, "", "", fmt.Errorf("day-of-week field: %w", err)
	}
	return cronSchedule{
		minute: minute, hour: hour, dayOfMonth: dayOfMonth,
		month: month, dayOfWeek: dayOfWeek, location: location,
	}, strings.Join(fields, " "), canonicalTimezone, nil
}

func parseCronField(value string, minimum, maximum int, names map[string]int, sundaySeven bool) (cronField, error) {
	field := cronField{allowed: make([]bool, maximum+1), wildcard: strings.HasPrefix(value, "*")}
	if value == "" {
		return field, fmt.Errorf("field is empty")
	}
	for _, item := range strings.Split(value, ",") {
		if item == "" {
			return field, fmt.Errorf("empty list item")
		}
		base, stepText, hasStep := strings.Cut(item, "/")
		step := 1
		if hasStep {
			if stepText == "" || strings.Contains(stepText, "/") {
				return field, fmt.Errorf("invalid step")
			}
			parsed, err := strconv.Atoi(stepText)
			if err != nil || parsed < 1 {
				return field, fmt.Errorf("step must be a positive integer")
			}
			step = parsed
		}
		start, end := minimum, maximum
		switch {
		case base == "*":
		case strings.Contains(base, "-"):
			left, right, ok := strings.Cut(base, "-")
			if !ok || left == "" || right == "" || strings.Contains(right, "-") {
				return field, fmt.Errorf("invalid range")
			}
			var err error
			start, err = parseCronValue(left, minimum, maximum, names)
			if err != nil {
				return field, err
			}
			end, err = parseCronValue(right, minimum, maximum, names)
			if err != nil {
				return field, err
			}
			if start > end {
				return field, fmt.Errorf("range start must not exceed range end")
			}
		default:
			parsed, err := parseCronValue(base, minimum, maximum, names)
			if err != nil {
				return field, err
			}
			start, end = parsed, parsed
			if hasStep {
				end = maximum
			}
		}
		for candidate := start; candidate <= end; candidate += step {
			if sundaySeven && candidate == 7 {
				field.allowed[0] = true
			} else {
				field.allowed[candidate] = true
			}
		}
	}
	for candidate := minimum; candidate <= maximum; candidate++ {
		if field.allowed[candidate] {
			return field, nil
		}
	}
	return field, fmt.Errorf("field selects no values")
}

func parseCronValue(value string, minimum, maximum int, names map[string]int) (int, error) {
	if named, ok := names[strings.ToUpper(value)]; ok {
		return named, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, fmt.Errorf("value %q must be between %d and %d", value, minimum, maximum)
	}
	return parsed, nil
}

func (schedule cronSchedule) Next(after time.Time) (time.Time, error) {
	candidate := after.UTC().Truncate(time.Minute).Add(time.Minute)
	limit := candidate.AddDate(cronSearchYears, 0, 0)
	for !candidate.After(limit) {
		if schedule.matches(candidate) {
			return candidate, nil
		}
		candidate = candidate.Add(time.Minute)
	}
	return time.Time{}, fmt.Errorf("cron has no matching instant within %d years", cronSearchYears)
}

func (schedule cronSchedule) matches(instant time.Time) bool {
	local := instant.In(schedule.location)
	if !schedule.minute.allowed[local.Minute()] || !schedule.hour.allowed[local.Hour()] ||
		!schedule.month.allowed[int(local.Month())] {
		return false
	}
	dayOfMonth := schedule.dayOfMonth.allowed[local.Day()]
	dayOfWeek := schedule.dayOfWeek.allowed[int(local.Weekday())]
	switch {
	case schedule.dayOfMonth.wildcard && schedule.dayOfWeek.wildcard:
		return true
	case schedule.dayOfMonth.wildcard:
		return dayOfWeek
	case schedule.dayOfWeek.wildcard:
		return dayOfMonth
	default:
		return dayOfMonth || dayOfWeek
	}
}
