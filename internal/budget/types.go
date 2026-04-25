package budget

import (
	"fmt"
	"strings"
	"time"

	"gomodel/internal/core"
)

const (
	PeriodHourlySeconds  int64 = 3600
	PeriodDailySeconds   int64 = 86400
	PeriodWeeklySeconds  int64 = 604800
	PeriodMonthlySeconds int64 = 2592000
)

// Budget stores one spend limit for one user path and reset period.
type Budget struct {
	UserPath      string     `json:"user_path" bson:"user_path"`
	PeriodSeconds int64      `json:"period_seconds" bson:"period_seconds"`
	Amount        float64    `json:"amount" bson:"amount"`
	Source        string     `json:"source,omitempty" bson:"source,omitempty"`
	LastResetAt   *time.Time `json:"last_reset_at,omitempty" bson:"last_reset_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at" bson:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at" bson:"updated_at"`
}

// Settings controls the calendar anchors used to find the active budget period.
// Values are interpreted in UTC.
type Settings struct {
	DailyResetHour     int       `json:"daily_reset_hour" bson:"daily_reset_hour"`
	DailyResetMinute   int       `json:"daily_reset_minute" bson:"daily_reset_minute"`
	WeeklyResetWeekday int       `json:"weekly_reset_weekday" bson:"weekly_reset_weekday"`
	WeeklyResetHour    int       `json:"weekly_reset_hour" bson:"weekly_reset_hour"`
	WeeklyResetMinute  int       `json:"weekly_reset_minute" bson:"weekly_reset_minute"`
	MonthlyResetDay    int       `json:"monthly_reset_day" bson:"monthly_reset_day"`
	MonthlyResetHour   int       `json:"monthly_reset_hour" bson:"monthly_reset_hour"`
	MonthlyResetMinute int       `json:"monthly_reset_minute" bson:"monthly_reset_minute"`
	UpdatedAt          time.Time `json:"updated_at" bson:"updated_at"`
}

// CheckResult describes one evaluated budget limit.
type CheckResult struct {
	Budget      Budget    `json:"budget"`
	PeriodStart time.Time `json:"period_start"`
	PeriodEnd   time.Time `json:"period_end"`
	Spent       float64   `json:"spent"`
	Remaining   float64   `json:"remaining"`
}

// ExceededError indicates a budget has already been exhausted.
type ExceededError struct {
	Result CheckResult
}

func (e *ExceededError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf(
		"budget exceeded for %s %s limit: spent %.6f of %.6f",
		e.Result.Budget.UserPath,
		PeriodLabel(e.Result.Budget.PeriodSeconds),
		e.Result.Spent,
		e.Result.Budget.Amount,
	)
}

// DefaultSettings returns the reset anchors used when no DB setting exists.
func DefaultSettings() Settings {
	return Settings{
		DailyResetHour:     0,
		DailyResetMinute:   0,
		WeeklyResetWeekday: int(time.Monday),
		WeeklyResetHour:    0,
		WeeklyResetMinute:  0,
		MonthlyResetDay:    1,
		MonthlyResetHour:   0,
		MonthlyResetMinute: 0,
	}
}

func NormalizeUserPath(raw string) (string, error) {
	path, err := core.NormalizeUserPath(raw)
	if err != nil {
		return "", err
	}
	if path == "" {
		return "/", nil
	}
	return path, nil
}

func NormalizeBudget(b Budget) (Budget, error) {
	path, err := NormalizeUserPath(b.UserPath)
	if err != nil {
		return Budget{}, err
	}
	b.UserPath = path
	if b.PeriodSeconds <= 0 {
		return Budget{}, fmt.Errorf("period_seconds must be greater than 0")
	}
	if b.Amount <= 0 {
		return Budget{}, fmt.Errorf("amount must be greater than 0")
	}
	b.Source = strings.TrimSpace(b.Source)
	if b.LastResetAt != nil {
		t := b.LastResetAt.UTC()
		b.LastResetAt = &t
	}
	now := time.Now().UTC()
	if b.CreatedAt.IsZero() {
		b.CreatedAt = now
	}
	b.UpdatedAt = now
	return b, nil
}

func ValidateSettings(settings Settings) error {
	if settings.DailyResetHour < 0 || settings.DailyResetHour > 23 {
		return fmt.Errorf("daily_reset_hour must be between 0 and 23")
	}
	if settings.DailyResetMinute < 0 || settings.DailyResetMinute > 59 {
		return fmt.Errorf("daily_reset_minute must be between 0 and 59")
	}
	if settings.WeeklyResetWeekday < int(time.Sunday) || settings.WeeklyResetWeekday > int(time.Saturday) {
		return fmt.Errorf("weekly_reset_weekday must be between 0 and 6")
	}
	if settings.WeeklyResetHour < 0 || settings.WeeklyResetHour > 23 {
		return fmt.Errorf("weekly_reset_hour must be between 0 and 23")
	}
	if settings.WeeklyResetMinute < 0 || settings.WeeklyResetMinute > 59 {
		return fmt.Errorf("weekly_reset_minute must be between 0 and 59")
	}
	if settings.MonthlyResetDay < 1 || settings.MonthlyResetDay > 31 {
		return fmt.Errorf("monthly_reset_day must be between 1 and 31")
	}
	if settings.MonthlyResetHour < 0 || settings.MonthlyResetHour > 23 {
		return fmt.Errorf("monthly_reset_hour must be between 0 and 23")
	}
	if settings.MonthlyResetMinute < 0 || settings.MonthlyResetMinute > 59 {
		return fmt.Errorf("monthly_reset_minute must be between 0 and 59")
	}
	return nil
}

func PeriodSeconds(period string) (int64, bool) {
	switch strings.ToLower(strings.TrimSpace(period)) {
	case "hour", "hourly", "hours":
		return PeriodHourlySeconds, true
	case "day", "daily", "days":
		return PeriodDailySeconds, true
	case "week", "weekly", "weeks":
		return PeriodWeeklySeconds, true
	case "month", "monthly", "months":
		return PeriodMonthlySeconds, true
	default:
		return 0, false
	}
}

func PeriodLabel(seconds int64) string {
	switch seconds {
	case PeriodHourlySeconds:
		return "hourly"
	case PeriodDailySeconds:
		return "daily"
	case PeriodWeeklySeconds:
		return "weekly"
	case PeriodMonthlySeconds:
		return "monthly"
	default:
		return fmt.Sprintf("%ds", seconds)
	}
}

func PeriodBounds(now time.Time, seconds int64, settings Settings) (time.Time, time.Time) {
	now = now.UTC()
	switch seconds {
	case PeriodHourlySeconds:
		start := now.Truncate(time.Hour)
		return start, start.Add(time.Hour)
	case PeriodDailySeconds:
		start := anchoredDayStart(now, settings.DailyResetHour, settings.DailyResetMinute)
		return start, start.AddDate(0, 0, 1)
	case PeriodWeeklySeconds:
		start := anchoredWeekStart(now, time.Weekday(settings.WeeklyResetWeekday), settings.WeeklyResetHour, settings.WeeklyResetMinute)
		return start, start.AddDate(0, 0, 7)
	case PeriodMonthlySeconds:
		start := anchoredMonthStart(now, settings.MonthlyResetDay, settings.MonthlyResetHour, settings.MonthlyResetMinute)
		nextMonth := time.Date(start.Year(), start.Month()+1, 1, 0, 0, 0, 0, time.UTC)
		return start, monthAnchor(nextMonth.Year(), nextMonth.Month(), settings.MonthlyResetDay, settings.MonthlyResetHour, settings.MonthlyResetMinute)
	default:
		if seconds <= 0 {
			return now, now
		}
		startUnix := now.Unix() - (now.Unix() % seconds)
		start := time.Unix(startUnix, 0).UTC()
		return start, start.Add(time.Duration(seconds) * time.Second)
	}
}

func anchoredDayStart(now time.Time, hour, minute int) time.Time {
	start := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, time.UTC)
	if now.Before(start) {
		start = start.AddDate(0, 0, -1)
	}
	return start
}

func anchoredWeekStart(now time.Time, weekday time.Weekday, hour, minute int) time.Time {
	todayAnchor := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, time.UTC)
	daysBack := (int(todayAnchor.Weekday()) - int(weekday) + 7) % 7
	start := todayAnchor.AddDate(0, 0, -daysBack)
	if now.Before(start) {
		start = start.AddDate(0, 0, -7)
	}
	return start
}

func anchoredMonthStart(now time.Time, day, hour, minute int) time.Time {
	start := monthAnchor(now.Year(), now.Month(), day, hour, minute)
	if now.Before(start) {
		prev := now.AddDate(0, -1, 0)
		start = monthAnchor(prev.Year(), prev.Month(), day, hour, minute)
	}
	return start
}

func monthAnchor(year int, month time.Month, day, hour, minute int) time.Time {
	anchorDay := min(day, daysInMonth(year, month))
	return time.Date(year, month, anchorDay, hour, minute, 0, 0, time.UTC)
}

func daysInMonth(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}
