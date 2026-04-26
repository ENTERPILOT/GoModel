package budget

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type settingsRowScanner interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}

func settingsKeyValues(settings Settings) map[string]int {
	return map[string]int{
		settingDailyResetHour:     settings.DailyResetHour,
		settingDailyResetMinute:   settings.DailyResetMinute,
		settingWeeklyResetWeekday: settings.WeeklyResetWeekday,
		settingWeeklyResetHour:    settings.WeeklyResetHour,
		settingWeeklyResetMinute:  settings.WeeklyResetMinute,
		settingMonthlyResetDay:    settings.MonthlyResetDay,
		settingMonthlyResetHour:   settings.MonthlyResetHour,
		settingMonthlyResetMinute: settings.MonthlyResetMinute,
	}
}

func applySettingValue(settings *Settings, key, value string) error {
	if settings == nil {
		return nil
	}
	key = strings.TrimSpace(key)
	var target *int
	switch key {
	case settingDailyResetHour:
		target = &settings.DailyResetHour
	case settingDailyResetMinute:
		target = &settings.DailyResetMinute
	case settingWeeklyResetWeekday:
		target = &settings.WeeklyResetWeekday
	case settingWeeklyResetHour:
		target = &settings.WeeklyResetHour
	case settingWeeklyResetMinute:
		target = &settings.WeeklyResetMinute
	case settingMonthlyResetDay:
		target = &settings.MonthlyResetDay
	case settingMonthlyResetHour:
		target = &settings.MonthlyResetHour
	case settingMonthlyResetMinute:
		target = &settings.MonthlyResetMinute
	default:
		return nil
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return fmt.Errorf("budget setting %s must be an integer", key)
	}
	*target = parsed
	return nil
}

func scanSettingsRows(rows settingsRowScanner) (Settings, error) {
	settings := DefaultSettings()
	var latest int64
	for rows.Next() {
		var key, value string
		var updatedAt int64
		if err := rows.Scan(&key, &value, &updatedAt); err != nil {
			return Settings{}, fmt.Errorf("scan budget setting: %w", err)
		}
		if err := applySettingValue(&settings, key, value); err != nil {
			return Settings{}, err
		}
		if updatedAt > latest {
			latest = updatedAt
		}
	}
	if err := rows.Err(); err != nil {
		return Settings{}, fmt.Errorf("iterate budget settings: %w", err)
	}
	if latest > 0 {
		settings.UpdatedAt = time.Unix(latest, 0).UTC()
	}
	return normalizeLoadedSettings(settings)
}
