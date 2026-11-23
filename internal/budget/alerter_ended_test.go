package budget

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
	"github.com/enterpilot/gomodel/internal/usage"
)

// A threshold first reached after the last pass of a period is alerted by the
// passes that follow the period's end, from that period's own spend.
func TestAlerterEvaluatesThePeriodThatJustEnded(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		usageStore, err := usage.NewSQLStore(ctx, db, 0)
		require.NoError(t, err)
		instance := newAlertInstance(t, db)
		require.NoError(t, instance.service.UpsertBudgets(ctx, []Budget{
			{Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodDailySeconds, Amount: 100, Source: SourceManual},
			{Scope: ScopeUserPath, Subject: "/customers", PerChild: true, PeriodSeconds: PeriodDailySeconds, Amount: 10, Source: SourceManual},
		}))
		// A midnight ahead of the wall clock: the budgets, stored at the wall
		// clock's time, are unchanged since.
		midnight := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, 2)

		// The last pass of the day finds nothing to alert.
		instance.now = midnight.Add(-50 * time.Second)
		spend(t, usageStore, "early", "/team", midnight.Add(-2*time.Hour), 70)
		assert.Empty(t, instance.evaluate(t))

		// Both budgets cross a threshold after it, and the new day has spend.
		spend(t, usageStore, "late", "/team", midnight.Add(-20*time.Second), 15)
		spend(t, usageStore, "late-child", "/customers/alice", midnight.Add(-10*time.Second), 10)
		spend(t, usageStore, "new-day", "/team", midnight.Add(5*time.Second), 1)

		instance.now = midnight.Add(10 * time.Second)
		require.NoError(t, instance.alerter.Evaluate(ctx))
		events := instance.recorder.take()
		assert.Equal(t, []string{"/team@80", "/customers/alice@80", "/customers/alice@100"}, alertNames(events))
		for _, event := range events {
			assert.Equal(t, midnight, event.PeriodEnd, "the alert is of the day that ended")
			assert.Equal(t, midnight.AddDate(0, 0, -1), event.PeriodStart)
			assert.Equal(t, instance.now, event.Timestamp)
			assert.Nil(t, event.ProjectedExhaustionAt)
		}
		assert.Equal(t, float64(85), events[0].Spent, "the new day's spend is not the ended day's")
		assert.Equal(t, float64(85), *events[0].ProjectedSpend, "an ended period is projected to what it spent")

		// Usage of the ended day that is saved late still counts...
		spend(t, usageStore, "saved-late", "/team", midnight.Add(-time.Second), 20)
		instance.now = midnight.Add(70 * time.Second)
		assert.Equal(t, []string{"/team@100"}, instance.evaluate(t))
		assert.Empty(t, instance.evaluate(t), "an ended period fires each alert once, too")

		// ...until the day is left behind.
		spend(t, usageStore, "saved-too-late", "/customers/bob", midnight.Add(-time.Second), 10)
		instance.now = midnight.Add(instance.alerter.endedFor)
		assert.Empty(t, instance.evaluate(t))
	})
}

// What a budget is now says nothing about a period that ended before it was
// reset, so that period is not evaluated for it.
func TestAlerterLeavesOutAnEndedPeriodOfAResetBudget(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		usageStore, err := usage.NewSQLStore(ctx, db, 0)
		require.NoError(t, err)
		instance := newAlertInstance(t, db)
		require.NoError(t, instance.service.UpsertBudgets(ctx, []Budget{
			{Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodDailySeconds, Amount: 100, Source: SourceManual},
		}))
		midnight := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, 2)

		spend(t, usageStore, "late", "/team", midnight.Add(-20*time.Second), 85)
		require.NoError(t, instance.service.ResetBudget(ctx, ScopeUserPath, "/team", PeriodDailySeconds, midnight.Add(5*time.Second)))
		instance.now = midnight.Add(10 * time.Second)
		assert.Empty(t, instance.evaluate(t))
	})
}

func TestAlerterJustEnded(t *testing.T) {
	alerter := &Alerter{endedFor: 150 * time.Second}
	hour := time.Date(2026, time.April, 25, 12, 0, 0, 0, time.UTC)
	budget := func(subject string, periodSeconds int64, updatedAt time.Time) Budget {
		return Budget{Scope: ScopeUserPath, Subject: subject, PeriodSeconds: periodSeconds, Amount: 1, UpdatedAt: updatedAt}
	}
	budgets := []Budget{
		budget("/hourly", PeriodHourlySeconds, hour.Add(-time.Minute)),
		budget("/ten-minutes", 600, hour.Add(-time.Hour)),
		budget("/daily", PeriodDailySeconds, hour.Add(-time.Hour)),     // its day ended twelve hours ago
		budget("/changed", PeriodHourlySeconds, hour.Add(time.Second)), // edited in the new hour
		budget("/changed-on-the-hour", PeriodHourlySeconds, hour),
	}

	subjects := func(now time.Time) map[time.Time][]string {
		ended := make(map[time.Time][]string)
		for end, budgets := range alerter.justEnded(budgets, now, DefaultSettings()) {
			for _, budget := range budgets {
				ended[end] = append(ended[end], budget.Subject)
			}
		}
		return ended
	}
	want := map[time.Time][]string{hour: {"/hourly", "/ten-minutes"}}
	assert.Equal(t, want, subjects(hour))
	assert.Equal(t, want, subjects(hour.Add(149*time.Second)))
	assert.Empty(t, subjects(hour.Add(150*time.Second)))
	assert.Empty(t, subjects(hour.Add(-time.Second)), "a period still running has not ended")
}
