package manager

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestProxyProfileServiceLifecycleAndRedaction(t *testing.T) {
	dir := t.TempDir()
	service := NewProxyProfileService()
	service.Configure(Config{DataDir: dir})
	applied := 0
	service.SetBindingApplier(func(context.Context, []Account, string, string) error { applied++; return nil })

	created, err := service.Create("Primary", "socks5h://user:pass@proxy.example:1080?token=secret", "primary", []string{"Codex", "codex"}, nil)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.ProxyURLMasked != "socks5h://proxy.example:1080" {
		t.Fatalf("masked = %q", created.ProxyURLMasked)
	}
	if len(created.Providers) != 1 || created.Providers[0] != "codex" || !created.Enabled {
		t.Fatalf("normalized = %#v", created)
	}

	raw, err := json.Marshal(service.List(context.Background()))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"pass", "secret", "frag", "user:"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("response leaked %q: %s", secret, raw)
		}
	}

	account := Account{ID: "account-1", Name: "a.json"}
	if err := service.BindWithKey([]Account{account}, created.ID, "management-key"); err != nil {
		t.Fatalf("BindWithKey() error = %v", err)
	}
	if applied != 1 {
		t.Fatalf("applied = %d", applied)
	}
	views := service.List(context.Background())
	if len(views) != 1 || views[0].AccountCount != 1 {
		t.Fatalf("views = %#v", views)
	}

	reloaded := NewProxyProfileService()
	reloaded.Configure(Config{DataDir: filepath.Join(dir)})
	views = reloaded.List(context.Background())
	if len(views) != 1 || views[0].ID != created.ID || views[0].AccountCount != 1 {
		t.Fatalf("reload = %#v", views)
	}

	if reloaded.Delete(created.ID, false) == nil {
		t.Fatal("Delete(force=false) did not fail while bound")
	}
	if err := reloaded.Delete(created.ID, true); err != nil {
		t.Fatalf("Delete(force=true) error = %v", err)
	}
}

func TestBatchPatchResolvesProxyProfile(t *testing.T) {
	resolver := func(id string) (string, bool) {
		if id == "profile-1" {
			return "http://resolved.internal:8080", true
		}
		return "", false
	}
	patch, err := BatchPatch{ProxyProfileID: stringPointer(" profile-1 ")}.ResolveProxyProfile(resolver)
	if err != nil {
		t.Fatal(err)
	}
	if patch.ProxyURL == nil || *patch.ProxyURL != "http://resolved.internal:8080" {
		t.Fatalf("patch = %#v", patch)
	}
	if patch.ProxyProfileID != nil {
		t.Fatalf("profile id remained")
	}
	if got, _ := patch.FieldPayload("x.json")["proxy_url"]; got != "http://resolved.internal:8080" {
		t.Fatalf("payload proxy_url = %#v", got)
	}
	missing := BatchPatch{ProxyProfileID: stringPointer("missing")}
	if _, resolveErr := missing.ResolveProxyProfile(resolver); resolveErr == nil {
		t.Fatal("Resolve(missing) did not fail")
	}
}
