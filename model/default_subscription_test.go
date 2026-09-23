package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func withDefaultSubscriptionPlanId(t *testing.T, planId int) {
	t.Helper()
	original := common.DefaultSubscriptionPlanId
	t.Cleanup(func() { common.DefaultSubscriptionPlanId = original })
	common.DefaultSubscriptionPlanId = planId
}

func seedDefaultSubscriptionPlan(t *testing.T, id int, enabled bool) *SubscriptionPlan {
	t.Helper()
	plan := &SubscriptionPlan{
		Id:               id,
		Title:            "Weekly",
		PriceAmount:      0,
		DurationUnit:     SubscriptionDurationYear,
		DurationValue:    10,
		TotalAmount:      1_000_000_000,
		QuotaResetPeriod: SubscriptionResetWeekly,
		Enabled:          enabled,
	}
	require.NoError(t, DB.Create(plan).Error)
	// Enabled carries gorm:"default:true", so Create skips the false zero value
	// and the column default wins; write it back explicitly.
	require.NoError(t, DB.Model(&SubscriptionPlan{}).Where("id = ?", id).Update("enabled", enabled).Error)
	return plan
}

func insertSubscriptionTestUser(t *testing.T, username string) *User {
	t.Helper()
	user := &User{Username: username, Password: "placeholder", DisplayName: username}
	require.NoError(t, user.Insert(0))
	return user
}

func activeSubscriptionsOf(t *testing.T, userId int) []UserSubscription {
	t.Helper()
	var subs []UserSubscription
	require.NoError(t, DB.Where("user_id = ?", userId).Find(&subs).Error)
	return subs
}

// Registration must hand the user their starting subscription in the same
// transaction, so no account can exist without the quota it is entitled to.
func TestRegistrationGrantsConfiguredDefaultSubscription(t *testing.T) {
	truncateTables(t)
	plan := seedDefaultSubscriptionPlan(t, 7301, true)
	withDefaultSubscriptionPlanId(t, plan.Id)

	user := insertSubscriptionTestUser(t, "sso_default_plan")

	subs := activeSubscriptionsOf(t, user.Id)
	require.Len(t, subs, 1)
	assert.Equal(t, plan.Id, subs[0].PlanId)
	assert.Equal(t, int64(1_000_000_000), subs[0].AmountTotal)
	assert.Equal(t, int64(0), subs[0].AmountUsed)
	assert.Equal(t, "active", subs[0].Status)
	assert.Equal(t, "auto", subs[0].Source)
	// A weekly plan must carry a reset deadline, otherwise the quota never refills.
	assert.Greater(t, subs[0].NextResetTime, int64(0))
}

// A misconfigured or withdrawn plan is an operator problem; refusing the
// registration would lock every SSO user out of a deployment that has no other
// login path.
func TestRegistrationSurvivesUnusableDefaultSubscriptionPlan(t *testing.T) {
	cases := []struct {
		name     string
		planId   func(t *testing.T) int
		username string
	}{
		{
			name:     "feature disabled",
			planId:   func(*testing.T) int { return 0 },
			username: "sso_no_default_plan",
		},
		{
			name:     "plan does not exist",
			planId:   func(*testing.T) int { return 7399 },
			username: "sso_missing_plan",
		},
		{
			name: "plan is disabled",
			planId: func(t *testing.T) int {
				return seedDefaultSubscriptionPlan(t, 7302, false).Id
			},
			username: "sso_disabled_plan",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			truncateTables(t)
			withDefaultSubscriptionPlanId(t, tc.planId(t))

			user := insertSubscriptionTestUser(t, tc.username)

			assert.NotZero(t, user.Id)
			assert.Empty(t, activeSubscriptionsOf(t, user.Id))
		})
	}
}

// InsertWithTx backs the OAuth registration path, so it has to grant the same
// subscription as the plain Insert path.
func TestInsertWithTxGrantsDefaultSubscription(t *testing.T) {
	truncateTables(t)
	plan := seedDefaultSubscriptionPlan(t, 7303, true)
	withDefaultSubscriptionPlanId(t, plan.Id)

	user := &User{Username: "sso_tx_plan", Password: "placeholder", DisplayName: "sso_tx_plan"}
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		return user.InsertWithTx(tx, 0)
	}))

	subs := activeSubscriptionsOf(t, user.Id)
	require.Len(t, subs, 1)
	assert.Equal(t, plan.Id, subs[0].PlanId)
}
