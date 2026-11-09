package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A community provider imported before this plugin ran sign-ins carries notes
// that told the operator to finish the login somewhere else. Those notes are
// stored, so the page must not keep rendering them: the provider it lists has to
// describe what the plugin does now.
func TestImportedProviderCopyFollowsTheInstalledPackage(t *testing.T) {
	store := NewImportStore(filepath.Join(t.TempDir(), "provider-imports.json"))
	entry, errImport := store.Import([]byte(`{"name":"@magpie-community/opencode-zcode-auth","version":"0.1.57","main":"./opencode.mjs"}`), "magpie", "")
	if errImport != nil {
		t.Fatalf("Import: %v", errImport)
	}
	if strings.Contains(strings.Join(LiveNotes(entry), " "), "not executed") {
		t.Fatalf("a manifest-only import still claims hooks run: %v", LiveNotes(entry))
	}
	// A manifest-only entry says what to do about it instead of pointing at
	// another application.
	if joined := strings.Join(LiveNotes(entry), " "); !strings.Contains(joined, "Re-import") {
		t.Fatalf("manifest-only notes = %v", LiveNotes(entry))
	}
	definitions := store.Definitions()
	if len(definitions) != 1 {
		t.Fatalf("definitions = %d", len(definitions))
	}
	if definitions[0].Capabilities.Login {
		t.Fatal("a manifest-only import advertised a sign-in it cannot run")
	}
	if strings.Contains(definitions[0].Description, "hooks are not executed") {
		t.Fatalf("description = %q", definitions[0].Description)
	}
}

// Once the package is on disk the provider advertises a sign-in, because this
// plugin is what runs it.
func TestImportedProviderAdvertisesSignInWhenInstalled(t *testing.T) {
	root := t.TempDir()
	store := NewImportStore(filepath.Join(root, "provider-imports.json"))
	packageDir := filepath.Join(store.PluginsDir(), "magpie-community__opencode-zcode-auth")
	if errMkdir := os.MkdirAll(packageDir, 0o700); errMkdir != nil {
		t.Fatalf("mkdir: %v", errMkdir)
	}
	entry, errImport := store.Import([]byte(`{"name":"@magpie-community/opencode-zcode-auth","version":"0.1.57","main":"./opencode.mjs"}`), "magpie", packageDir)
	if errImport != nil {
		t.Fatalf("Import: %v", errImport)
	}
	definitions := store.Definitions()
	if len(definitions) != 1 || !definitions[0].Capabilities.Login {
		t.Fatalf("definitions = %#v", definitions)
	}
	if joined := strings.Join(LiveNotes(entry), " "); !strings.Contains(joined, "sign-in runs here") {
		t.Fatalf("installed notes = %v", LiveNotes(entry))
	}
	if strings.Contains(definitions[0].Description, "cannot run here") {
		t.Fatalf("description = %q", definitions[0].Description)
	}
}
