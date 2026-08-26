package manager

import (
	"context"
	"errors"
)

type ProxyBindingRequest struct {
	Scope         TargetScope `json:"scope"`
	ProxyProfileID string     `json:"proxy_profile_id"`
}

func (a *App) applyProxyProfileBindings(ctx context.Context, accounts []Account, proxyURL string, managementKey string) error {
	if len(accounts) == 0 { return nil }
	config := a.configSnapshot()
	client, errClient := newManagementClient(resolveManagementBaseURL(config.ManagementBaseURL), managementKey, a.managementDoer)
	if errClient != nil { return errors.New("management API is unavailable") }
	defer client.clearSecrets()
	for _, account := range accounts {
		if ctx.Err() != nil { return ctx.Err() }
		if !safeAuthJSONName(account.Name) { return errors.New("account auth file is invalid") }
		patch := BatchPatch{}
		if proxyURL == "" { patch.ProxyURL = stringPointer("") } else { patch.ProxyURL = stringPointer(proxyURL) }
		if errPatch := client.PatchFields(ctx, account.Name, patch); errPatch != nil { return errors.New("proxy profile update failed") }
	}
	return nil
}
