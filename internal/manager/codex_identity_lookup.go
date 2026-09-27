package manager

import (
	"context"
	"fmt"
	"strings"

	"cpa-account-config-manager/internal/cpaapi"
)

// identityAccount resolves only the selected credential. The request path must
// not use the management list's pagination, usage snapshots or detail fan-out.
func (s *AccountService) identityAccount(ctx context.Context, identity string) (Account, currentAuthDocument, error) {
	if s == nil || s.host == nil {
		return Account{}, currentAuthDocument{}, fmt.Errorf("auth host is unavailable")
	}
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return Account{}, currentAuthDocument{}, ErrAccountConfigNotFound
	}
	entry, errEntry := s.identityAccountEntry(ctx, identity)
	if errEntry != nil {
		return Account{}, currentAuthDocument{}, errEntry
	}
	// This projection is private to request interception. Do not infer edit
	// eligibility from one record: duplicate-file checks belong to the UI list.
	account := projectHostEntry(entry, nil, nil, nil)
	if isPluginOwnedAuthEntry(entry) || isPluginOwnedAccountProjection(account) {
		return Account{}, currentAuthDocument{}, ErrAccountConfigNotFound
	}
	if errContext := ctx.Err(); errContext != nil {
		return Account{}, currentAuthDocument{}, errContext
	}
	detail, errGet := s.host.GetAuth(ctx, account.ID)
	if errGet != nil {
		return Account{}, currentAuthDocument{}, fmt.Errorf("read selected auth file: %w", errGet)
	}
	if errContext := ctx.Err(); errContext != nil {
		return Account{}, currentAuthDocument{}, errContext
	}
	document, errDocument := authDocumentFromDetail(account, detail)
	if errDocument != nil {
		return Account{}, currentAuthDocument{}, errDocument
	}
	// Preserve fields such as device_id used by account fingerprinting, while
	// reusing the same physical read that supplied the restriction metadata.
	if errEnrich := enrichAccount(&account, detail); errEnrich != nil {
		return Account{}, currentAuthDocument{}, errEnrich
	}
	return account, document, nil
}

func (s *AccountService) identityAccountEntry(ctx context.Context, identity string) (cpaapi.HostAuthFileEntry, error) {
	if errContext := ctx.Err(); errContext != nil {
		return cpaapi.HostAuthFileEntry{}, errContext
	}
	if runtimeHost, ok := s.host.(RuntimeAuthHost); ok {
		entry, errRuntime := runtimeHost.GetAuthRuntime(ctx, identity)
		if errRuntime == nil && identityMatchesHostEntry(entry, identity) {
			return entry, nil
		}
	}
	if errContext := ctx.Err(); errContext != nil {
		return cpaapi.HostAuthFileEntry{}, errContext
	}
	// Older hosts lack get_runtime, and some callers still send the credential
	// ID instead of auth_index. Resolve the alias from the lightweight list,
	// without fetching any unrelated physical documents or truncating at 1000.
	entries, errList := s.host.ListAuth(ctx)
	if errList != nil {
		return cpaapi.HostAuthFileEntry{}, fmt.Errorf("resolve selected auth: %w", errList)
	}
	for _, entry := range entries {
		if identityMatchesHostEntry(entry, identity) && !isPluginOwnedAuthEntry(entry) {
			return entry, nil
		}
	}
	return cpaapi.HostAuthFileEntry{}, ErrAccountConfigNotFound
}

func identityMatchesHostEntry(entry cpaapi.HostAuthFileEntry, identity string) bool {
	return strings.TrimSpace(entry.AuthIndex) == identity || strings.TrimSpace(entry.ID) == identity
}
