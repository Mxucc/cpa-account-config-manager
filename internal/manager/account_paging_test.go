package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"cpa-account-config-manager/internal/cpaapi"
)

// pagedAuthHostFixture builds a host whose listing is larger than one
// management page, so a caller that reads a single capped page silently drops
// every account past the first 1000.
func pagedAuthHostFixture(size int) *fakeAuthHost {
	host := &fakeAuthHost{details: make(map[string]cpaapi.HostAuthGetResponse, size)}
	for i := 0; i < size; i++ {
		index := fmt.Sprintf("auth-%04d", i)
		name := fmt.Sprintf("account-%04d.json", i)
		host.entries = append(host.entries, cpaapi.HostAuthFileEntry{
			AuthIndex: index, ID: name, Name: name, Provider: "codex", Type: "codex",
			AccountType: "oauth", Source: "file", Path: "/auths/" + name,
		})
		host.details[index] = cpaapi.HostAuthGetResponse{
			AuthIndex: index, Name: name, Path: "/auths/" + name,
			JSON: json.RawMessage(`{"type":"codex","codex_cli_only":false}`),
		}
	}
	return host
}

func TestListAllAccountsCoversEveryPage(t *testing.T) {
	const size = maxPageSize + 500
	host := pagedAuthHostFixture(size)
	accounts, errList := NewAccountService(host).ListAllAccounts(context.Background())
	if errList != nil {
		t.Fatalf("ListAllAccounts: %v", errList)
	}
	if len(accounts) != size {
		t.Fatalf("ListAllAccounts returned %d accounts, want %d", len(accounts), size)
	}
	last := fmt.Sprintf("auth-%04d", size-1)
	if accounts[len(accounts)-1].ID != last {
		t.Fatalf("last account = %q, want %q", accounts[len(accounts)-1].ID, last)
	}
}

// A provider that ignores the requested page answers with a short page, which
// must end the walk instead of repeating the same rows forever.
func TestWalkAccountPagesStopsOnAProviderThatIgnoresThePage(t *testing.T) {
	fetches := 0
	rows := []Account{{ID: "one"}, {ID: "two"}}
	errWalk := walkAccountPages(context.Background(), maxPageSize, func(int) (ListResponse, error) {
		fetches++
		return ListResponse{Accounts: rows}, nil
	}, nil)
	if errWalk != nil {
		t.Fatalf("walkAccountPages: %v", errWalk)
	}
	if fetches != 1 {
		t.Fatalf("walkAccountPages fetched %d pages from a page-ignoring provider, want 1", fetches)
	}
}

// The retry budget is stored per credential, so it must reach every account an
// operator sees, not only the first page the host serves.
func TestAutoRetryApplyCoversAccountsPastTheFirstPage(t *testing.T) {
	const size = maxPageSize + 200
	host := pagedAuthHostFixture(size)
	stub := &autoRetryHostStub{requestRetry: 3, retryInterval: 30}
	app, _ := newAutoRetryTestApp(t, stub, host)

	if updated := app.applyAutoRetryToCodexAuthFiles(context.Background(), 4); updated != size {
		t.Fatalf("applyAutoRetryToCodexAuthFiles updated %d accounts, want %d", updated, size)
	}
	lastIndex := fmt.Sprintf("auth-%04d", size-1)
	host.mu.Lock()
	last := append(json.RawMessage(nil), host.details[lastIndex].JSON...)
	host.mu.Unlock()
	if !strings.Contains(string(last), `"request_retry":4`) {
		t.Fatalf("the account past the first page kept its previous budget: %s", last)
	}
}

// Document reads keep the account list's bounded concurrency and degrade per
// account: an unreadable credential is omitted instead of failing the caller.
func TestReadAuthDocumentsSkipsUnreadableAccounts(t *testing.T) {
	host := pagedAuthHostFixture(5)
	host.errors = map[string]error{"auth-0002": errors.New("read failed")}
	service := NewAccountService(host)
	accounts, errList := service.ListAllAccounts(context.Background())
	if errList != nil {
		t.Fatalf("ListAllAccounts: %v", errList)
	}
	documents := service.ReadAuthDocuments(context.Background(), accounts)
	if len(documents) != len(accounts)-1 {
		t.Fatalf("ReadAuthDocuments returned %d documents, want %d", len(documents), len(accounts)-1)
	}
	if _, exists := documents["auth-0002"]; exists {
		t.Fatal("an unreadable credential was reported as read")
	}
	for _, account := range accounts {
		if account.ID == "auth-0002" {
			continue
		}
		if _, exists := documents[account.ID]; !exists {
			t.Fatalf("credential %s was not read", account.ID)
		}
	}
	if len(service.ReadAuthDocuments(context.Background(), nil)) != 0 {
		t.Fatal("an empty account set must not produce documents")
	}
}
