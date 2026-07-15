package manager

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

func TestPreviewCreatesFixedRedactedTargetSnapshot(t *testing.T) {
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{
			{AuthIndex: "editable", Name: "editable.json", Provider: "codex", Source: "file", Path: "/auths/editable.json"},
			{AuthIndex: "runtime", Name: "runtime", Provider: "gemini", Source: "runtime", RuntimeOnly: true},
		},
		details: map[string]cpaapi.HostAuthGetResponse{
			"editable": {AuthIndex: "editable", Name: "editable.json", Path: "/auths/editable.json", JSON: json.RawMessage(`{"access_token":"auth-secret"}`)},
		},
	}
	service := NewPreviewService(NewAccountService(host))
	service.now = func() time.Time { return time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC) }
	proxyURL := "http://user:proxy-secret@127.0.0.1:7890"
	preview, errCreate := service.Create(context.Background(), PreviewRequest{
		Scope: TargetScope{Mode: "selected", IDs: []string{"editable", "runtime", "missing"}},
		Patch: BatchPatch{
			ProxyURL: &proxyURL,
			Headers:  &HeaderPatch{Set: map[string]string{"Authorization": "Bearer header-secret"}},
		},
	})
	if errCreate != nil {
		t.Fatalf("Create() error = %v", errCreate)
	}
	if preview.Total != 3 || preview.Eligible != 1 || preview.ReadOnly != 1 || preview.Missing != 1 || preview.PhysicalFiles != 1 {
		t.Fatalf("preview counts = %#v", preview)
	}
	encoded, errMarshal := json.Marshal(preview)
	if errMarshal != nil {
		t.Fatalf("Marshal() error = %v", errMarshal)
	}
	for _, secret := range []string{"auth-secret", "proxy-secret", "Bearer header-secret"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("preview leaked %q: %s", secret, encoded)
		}
	}

	host.entries = append(host.entries, cpaapi.HostAuthFileEntry{
		AuthIndex: "later", Name: "later.json", Provider: "codex", Source: "file", Path: "/auths/later.json",
	})
	stored, errGet := service.Get(preview.ID)
	if errGet != nil {
		t.Fatalf("Get() error = %v", errGet)
	}
	if len(stored.Targets) != 1 || stored.Targets[0].ID != "editable" {
		t.Fatalf("stored targets changed = %#v", stored.Targets)
	}
}

func TestPreviewExpiresAndDropsInMemoryPatch(t *testing.T) {
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{{AuthIndex: "a", Name: "a.json", Source: "file", Path: "/auths/a.json"}},
		details: map[string]cpaapi.HostAuthGetResponse{
			"a": {AuthIndex: "a", Name: "a.json", Path: "/auths/a.json", JSON: json.RawMessage(`{"type":"codex"}`)},
		},
	}
	service := NewPreviewService(NewAccountService(host))
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	disabled := true
	preview, errCreate := service.Create(context.Background(), PreviewRequest{
		Scope: TargetScope{Mode: "selected", IDs: []string{"a"}},
		Patch: BatchPatch{Disabled: &disabled},
	})
	if errCreate != nil {
		t.Fatalf("Create() error = %v", errCreate)
	}
	now = now.Add(defaultPreviewTTL)
	_, errGet := service.Get(preview.ID)
	if !errors.Is(errGet, ErrPreviewExpired) {
		t.Fatalf("Get() error = %v, want ErrPreviewExpired", errGet)
	}
	_, errSecondGet := service.Get(preview.ID)
	if !errors.Is(errSecondGet, ErrPreviewNotFound) {
		t.Fatalf("second Get() error = %v, want ErrPreviewNotFound", errSecondGet)
	}
}
