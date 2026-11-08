package provider

import (
	"context"
	"encoding/json"
	"testing"
)

type stubSource struct {
	accounts []Account
}

func (s stubSource) Accounts(context.Context) ([]Account, error) {
	return s.accounts, nil
}

func (s stubSource) AccountByAuthIndex(context.Context, string) (Account, bool, error) {
	if len(s.accounts) == 0 {
		return Account{}, false, nil
	}
	return s.accounts[0], true, nil
}

// A provider that reported no window must answer with an empty list. Every page
// iterates the quota list, so a null would crash the client instead of showing
// "not reported".
func TestAccountsMarshalsAnEmptyQuotaListWhenNothingWasReported(t *testing.T) {
	service := NewService(stubSource{accounts: []Account{
		{ID: AccountID("codex", "acct-1"), Provider: ProviderRef{ID: "codex"}, Source: Source{Kind: "cpa-auth"}},
		{ID: AccountID("codex", "acct-2"), Provider: ProviderRef{ID: "codex"}, Quota: []Window{}, Source: Source{Kind: "cpa-auth"}},
	}})

	for _, account := range service.Accounts(context.Background()) {
		raw, errMarshal := json.Marshal(account)
		if errMarshal != nil {
			t.Fatalf("marshal %s: %v", account.ID, errMarshal)
		}
		var decoded map[string]any
		if errUnmarshal := json.Unmarshal(raw, &decoded); errUnmarshal != nil {
			t.Fatalf("unmarshal %s: %v", account.ID, errUnmarshal)
		}
		quota, found := decoded["quota"]
		if !found {
			t.Fatalf("%s has no quota key: %s", account.ID, raw)
		}
		windows, isList := quota.([]any)
		if !isList {
			t.Fatalf("%s serialized quota as %T, want a list: %s", account.ID, quota, raw)
		}
		if len(windows) != 0 {
			t.Fatalf("%s serialized %d windows, want none", account.ID, len(windows))
		}
	}
}

// A single-account read goes through the same normalization, so the detail page
// cannot receive a null list either.
func TestAccountMarshalsAnEmptyQuotaListForOneAccount(t *testing.T) {
	service := NewService(stubSource{accounts: []Account{
		{ID: AccountID("opencode-go", "wrk_1"), Provider: ProviderRef{ID: "opencode-go"}},
	}})
	account, ok := service.Account(context.Background(), AccountID("opencode-go", "wrk_1"))
	if !ok {
		t.Fatal("account was not found")
	}
	if account.Quota == nil {
		t.Fatal("quota list is nil; it must be an empty slice so JSON sends []")
	}
}
