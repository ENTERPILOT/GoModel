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
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return fmt.Errorf("budget setting %s must be an integer", key)
	}
	switch strings.TrimSpace(key) {
	case settingDailyResetHour:
		settings.DailyResetHour = parsed
	case settingDailyResetMinute:
		settings.DailyResetMinute = parsed
	case settingWeeklyResetWeekday:
		settings.WeeklyResetWeekday = parsed
	case settingWeeklyResetHour:
		settings.WeeklyResetHour = parsed
	case settingWeeklyResetMinute:
		settings.WeeklyResetMinute = parsed
	case settingMonthlyResetDay:
		settings.MonthlyResetDay = parsed
	case settingMonthlyResetHour:
		settings.MonthlyResetHour = parsed
	case settingMonthlyResetMinute:
		settings.MonthlyResetMinute = parsed
	default:
		return nil
	}
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
