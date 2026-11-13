package budget

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
	"github.com/enterpilot/gomodel/internal/usage"
)

// A child that spends mostly through paths below it must alert at the spend
// enforcement counts, even when the template has more children than one
// evaluation reads.
func TestAlerterCountsDeepChildrenAtTheCap(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		usageStore, err := usage.NewSQLStore(ctx, db, 0)
		require.NoError(t, err)
		instance := newAlertInstance(t, db)
		instance.alerter.childLimit = 3
		require.NoError(t, instance.service.UpsertBudgets(ctx, []Budget{
			{Scope: ScopeUserPath, Subject: "/c", PerChild: true, PeriodSeconds: PeriodDailySeconds, Amount: 10, AlertThresholds: []int{100}, Source: SourceManual},
		}))
		var entries []*usage.UsageEntry
		for i, row := range deepChildUsage() {
			entries = append(entries, usageEntryWithCost(fmt.Sprintf("deep-%d", i), row.path, "", alertTestNow.Add(-time.Hour), row.cost))
		}
		require.NoError(t, usageStore.WriteBatch(ctx, entries))

		require.NoError(t, instance.alerter.Evaluate(ctx))
		events := instance.recorder.take()
		require.Equal(t, []string{"/c/a@100"}, alertNames(events))

		statuses, err := instance.service.StatusesFor(ctx, Subjects{UserPath: "/c/a/k01/deeper/still"}, instance.now)
		require.NoError(t, err)
		require.Len(t, statuses, 1)
		assert.Equal(t, statuses[0].Spent, events[0].Spent, "the alert sees what enforcement counts")
		assert.Equal(t, float64(10), events[0].Spent)
	})
}

// revisionCreatedAt is when the budgets of the revision tests were created.
var revisionCreatedAt = alertTestNow.Add(-24 * time.Hour)

func revisionBudget(amount float64, thresholds ...int) Budget {
	return Budget{
		Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodDailySeconds, Amount: amount,
		AlertThresholds: thresholds, Source: SourceManual, CreatedAt: revisionCreatedAt,
	}
}

// Every edit of a budget mid-period, starting from an amount of 100 with 85
// spent and the 80% alert fired.
func TestAlerterRearmsOnANewBudgetRevision(t *testing.T) {
	tests := []struct {
		name string
		edit func(t *testing.T, service *Service)
		// wantAfterEdit fires on the first pass after the edit;
		// wantAfterSpend once 800 more is spent.
		wantAfterEdit  []string
		wantAfterSpend []string
		newRevision    bool
	}{
		{
			name:           "amount raised: lower thresholds fire again at the new amount",
			edit:           upsertRevision(revisionBudget(1000)),
			wantAfterSpend: []string{"/team@80"}, // 885 of 1000
			newRevision:    true,
		},
		{
			name:           "amount lowered: everything already reached fires at once",
			edit:           upsertRevision(revisionBudget(50)),
			wantAfterEdit:  []string{"/team@80", "/team@100"}, // 85 of 50
			wantAfterSpend: []string{},
			newRevision:    true,
		},
		{
			name: "thresholds edited: only a threshold not yet fired can fire",
			edit: upsertRevision(revisionBudget(100, 50, 80, 90)),
			// 50 is new and reached; 80 already fired against this revision.
			wantAfterEdit:  []string{"/team@50"},
			wantAfterSpend: []string{"/team@90"},
		},
		{
			name: "deleted and recreated: a new budget alerts from scratch",
			edit: func(t *testing.T, service *Service) {
				ctx := context.Background()
				require.NoError(t, service.DeleteBudget(ctx, ScopeUserPath, "/team", PeriodDailySeconds))
				recreated := revisionBudget(100)
				recreated.CreatedAt = alertTestNow.Add(-time.Minute)
				require.NoError(t, service.UpsertBudgets(ctx, []Budget{recreated}))
			},
			wantAfterEdit:  []string{"/team@80"},
			wantAfterSpend: []string{"/team@100"},
			newRevision:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
				ctx := context.Background()
				usageStore, err := usage.NewSQLStore(ctx, db, 0)
				require.NoError(t, err)
				first, second := newAlertInstance(t, db), newAlertInstance(t, db)
				require.NoError(t, first.service.UpsertBudgets(ctx, []Budget{revisionBudget(100)}))
				spend(t, usageStore, "before", "/team", alertTestNow.Add(-2*time.Hour), 85)
				require.NoError(t, first.alerter.Evaluate(ctx))
				before := first.recorder.take()
				require.Equal(t, []string{"/team@80"}, alertNames(before))

				tt.edit(t, first.service)
				require.NoError(t, second.service.Refresh(ctx))
				require.NoError(t, first.alerter.Evaluate(ctx))
				afterEdit := first.recorder.take()
				assert.Equal(t, nonNil(tt.wantAfterEdit), alertNames(afterEdit), "after the edit")
				assert.Empty(t, second.evaluate(t), "the other replica agrees on the revision")

				spend(t, usageStore, "after", "/team", alertTestNow.Add(-time.Hour), 800)
				require.NoError(t, first.alerter.Evaluate(ctx))
				afterSpend := first.recorder.take()
				assert.Equal(t, nonNil(tt.wantAfterSpend), alertNames(afterSpend), "after more spend")
				assert.Empty(t, second.evaluate(t))

				for _, event := range append(afterEdit, afterSpend...) {
					if event.ThresholdPercent != 80 {
						continue
					}
					assert.NotEqual(t, before[0].ID, event.ID, "an alert of another revision has another ID")
					assert.NotEqual(t, before[0].Budget.Revision, event.Budget.Revision)
				}
				budgets := first.service.Budgets()
				require.Len(t, budgets, 1)
				assert.Equal(t, tt.newRevision, budgets[0].alertRevision() != before[0].Budget.Revision)
			})
		})
	}
}

func upsertRevision(budget Budget) func(*testing.T, *Service) {
	return func(t *testing.T, service *Service) {
		require.NoError(t, service.UpsertBudgets(context.Background(), []Budget{budget}))
	}
}

// nonNil lets an expectation of no alerts compare equal to alertNames.
func nonNil(names []string) []string {
	if names == nil {
		return []string{}
	}
	return names
}

// Moving the period anchor mid-period starts a new period: the window is
// recomputed from the new anchor, and its thresholds fire again once the
// spend inside it reaches them.
func TestAlerterFollowsAMovedPeriodAnchor(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		usageStore, err := usage.NewSQLStore(ctx, db, 0)
		require.NoError(t, err)
		instance := newAlertInstance(t, db)
		require.NoError(t, instance.service.UpsertBudgets(ctx, []Budget{
			{Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodDailySeconds, Amount: 10, Source: SourceManual},
		}))
		day := time.Date(2026, time.April, 25, 0, 0, 0, 0, time.UTC)
		spend(t, usageStore, "night", "/team", day.Add(3*time.Hour), 4)
		spend(t, usageStore, "morning", "/team", day.Add(10*time.Hour), 5)

		require.NoError(t, instance.alerter.Evaluate(ctx))
		before := instance.recorder.take()
		require.Equal(t, []string{"/team@80"}, alertNames(before))
		assert.Equal(t, day, before[0].PeriodStart)

		settings := instance.service.Settings()
		settings.DailyResetHour = 6
		_, err = instance.service.SaveSettings(ctx, settings)
		require.NoError(t, err)
		assert.Empty(t, instance.evaluate(t), "5 of 10 since 06:00 reaches no threshold")

		spend(t, usageStore, "noon", "/team", day.Add(11*time.Hour), 3.5)
		require.NoError(t, instance.alerter.Evaluate(ctx))
		after := instance.recorder.take()
		require.Equal(t, []string{"/team@80"}, alertNames(after))
		assert.Equal(t, day.Add(6*time.Hour), after[0].PeriodStart)
		assert.NotEqual(t, before[0].ID, after[0].ID)
	})
}
