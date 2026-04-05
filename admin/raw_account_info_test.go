package admin

import "testing"

func TestExtractCredentialUpdatesFromRawInfo(t *testing.T) {
	raw := []byte(`{
		"account": {
			"email": "demo@example.com",
			"chatgpt_account_id": "acc_123",
			"plan_type": "Plus"
		}
	}`)

	refreshed, updates := extractCredentialUpdatesFromRawInfo(raw)

	if got := refreshed["email"]; got != "demo@example.com" {
		t.Fatalf("email = %q, want demo@example.com", got)
	}
	if got := refreshed["account_id"]; got != "acc_123" {
		t.Fatalf("account_id = %q, want acc_123", got)
	}
	if got := refreshed["plan_type"]; got != "plus" {
		t.Fatalf("plan_type = %q, want plus", got)
	}

	if got, _ := updates["email"].(string); got != "demo@example.com" {
		t.Fatalf("updates[email] = %q, want demo@example.com", got)
	}
	if got, _ := updates["account_id"].(string); got != "acc_123" {
		t.Fatalf("updates[account_id] = %q, want acc_123", got)
	}
	if got, _ := updates["plan_type"].(string); got != "plus" {
		t.Fatalf("updates[plan_type] = %q, want plus", got)
	}
}

func TestExtractCredentialUpdatesFromRawInfo_AccountsArray(t *testing.T) {
	raw := []byte(`{
		"account_ordering": ["b68fdf8a-5017-485a-9b94-362323c9fff2"],
		"accounts": [
			{
				"id": "b68fdf8a-5017-485a-9b94-362323c9fff2",
				"plan_type": "free"
			}
		],
		"default_account_id": "b68fdf8a-5017-485a-9b94-362323c9fff2"
	}`)

	refreshed, _ := extractCredentialUpdatesFromRawInfo(raw)

	if got := refreshed["account_id"]; got != "b68fdf8a-5017-485a-9b94-362323c9fff2" {
		t.Fatalf("account_id = %q, want default_account_id", got)
	}
	if got := refreshed["plan_type"]; got != "free" {
		t.Fatalf("plan_type = %q, want free", got)
	}
}

func TestMergeCredentialRefresh_PreferHigherPlan(t *testing.T) {
	profile := cliproxyProfile{
		Email:      "from_cpa@example.com",
		AccountID:  "acc_from_cpa",
		PlanType:   "plus",
		PlanSource: "id_token.chatgpt_plan_type",
	}
	upstream := map[string]string{
		"plan_type": "free",
	}

	refreshed, updates := mergeCredentialRefresh(profile, upstream, "free")

	if got := refreshed["plan_type"]; got != "plus" {
		t.Fatalf("refreshed plan_type = %q, want plus", got)
	}
	if got, _ := updates["plan_type"].(string); got != "plus" {
		t.Fatalf("updates plan_type = %q, want plus", got)
	}
	if got := refreshed["email"]; got != "from_cpa@example.com" {
		t.Fatalf("email = %q, want from_cpa@example.com", got)
	}
}
