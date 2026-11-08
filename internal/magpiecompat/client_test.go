package magpiecompat

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixtureDir(t *testing.T) string {
	t.Helper()
	absolute, errAbs := filepath.Abs(filepath.Join("testdata", "fixture-plugin"))
	if errAbs != nil {
		t.Fatalf("fixture path: %v", errAbs)
	}
	return absolute
}

// shRunner builds a runner whose "bun" is a shell command, so the timeout, crash
// and output-cap paths can be exercised without a plugin.
func shRunner(t *testing.T, script string, options Options) *Runner {
	t.Helper()
	runner := New("/bin/sh", "", options)
	runner.commandFactory = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sh", "-c", script)
	}
	return runner
}

func TestProbeReadsModelsAndWindowsFromARealPlugin(t *testing.T) {
	if !BunAvailable() {
		t.Skip("bun is not installed; the real-plugin path cannot be exercised here")
	}
	runner := New("", "", Options{Timeout: 30 * time.Second})
	result, errProbe := runner.Probe(context.Background(), fixtureDir(t))
	if errProbe != nil {
		t.Fatalf("Probe() error = %v", errProbe)
	}
	if result.ID != "fixture" {
		t.Fatalf("id = %q, want fixture", result.ID)
	}
	wantModels := []string{"fx-alpha", "fx-beta", "fx-gamma"}
	if strings.Join(result.Models, ",") != strings.Join(wantModels, ",") {
		t.Fatalf("models = %v, want %v (deduplicated, ordered)", result.Models, wantModels)
	}
	if len(result.Methods) != 2 || result.Methods[0] != "Device code" || result.Methods[1] != "API key" {
		t.Fatalf("methods = %v", result.Methods)
	}
	// Three windows came back: a nameless one is dropped, an aside one is kept
	// with its flag so routing can ignore it while the page still shows it.
	if len(result.Windows) != 3 {
		t.Fatalf("windows = %#v, want three named windows", result.Windows)
	}
	if !result.Windows[2].Aside || result.Windows[2].Name != "aside-window" {
		t.Fatalf("aside window = %#v", result.Windows[2])
	}
	first := result.Windows[0]
	if first.Name != "five_hour" || first.Used == nil || *first.Used != 1.5 || first.Limit == nil || *first.Limit != 10 {
		t.Fatalf("first window = %#v", first)
	}
	second := result.Windows[1]
	if second.Balance == nil || *second.Balance != 4.25 || second.Used != nil {
		t.Fatalf("wallet window must carry a balance and no used share: %#v", second)
	}
}

func TestProbeTimeoutKillsTheSidecar(t *testing.T) {
	runner := shRunner(t, "sleep 5", Options{Timeout: 200 * time.Millisecond})
	started := time.Now()
	_, errProbe := runner.Probe(context.Background(), fixtureDir(t))
	if !errors.Is(errProbe, ErrTimeout) {
		t.Fatalf("error = %v, want ErrTimeout", errProbe)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("probe waited %s; the timeout must kill the process", elapsed)
	}
}

func TestProbeCrashIsIsolated(t *testing.T) {
	runner := shRunner(t, "exit 3", Options{Timeout: 5 * time.Second})
	_, errProbe := runner.Probe(context.Background(), fixtureDir(t))
	if !errors.Is(errProbe, ErrPluginInvalid) {
		t.Fatalf("error = %v, want ErrPluginInvalid", errProbe)
	}
	// A crash must not poison the next call: a fresh runner still works.
	okRunner := shRunner(t, `printf '%s\n' '{"ok":true,"result":{"id":"after-crash"}}'`, Options{})
	result, errOK := okRunner.Probe(context.Background(), fixtureDir(t))
	if errOK != nil || result.ID != "after-crash" {
		t.Fatalf("follow-up probe = %#v, %v", result, errOK)
	}
}

func TestProbeRejectsOversizedOutput(t *testing.T) {
	// 8 KiB of garbage on stdout, with a 1 KiB cap.
	runner := shRunner(t, "i=0; while [ $i -lt 200 ]; do printf '0123456789012345678901234567890123456789\\n'; i=$((i+1)); done", Options{MaxOutput: 1024, Timeout: 5 * time.Second})
	_, errProbe := runner.Probe(context.Background(), fixtureDir(t))
	if !errors.Is(errProbe, ErrOutputTooLarge) {
		t.Fatalf("error = %v, want ErrOutputTooLarge", errProbe)
	}
}

func TestProbeWithoutBunDegradesInsteadOfFailingHard(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	runner := New("", "", Options{})
	if runner.Available() {
		t.Fatalf("no runtime should resolve with an empty PATH")
	}
	_, errProbe := runner.Probe(context.Background(), fixtureDir(t))
	if !errors.Is(errProbe, ErrBunUnavailable) {
		t.Fatalf("error = %v, want ErrBunUnavailable", errProbe)
	}
}

func TestProbeRejectsNonAbsoluteDirectory(t *testing.T) {
	runner := shRunner(t, "true", Options{})
	if _, errProbe := runner.Probe(context.Background(), "relative/dir"); errProbe == nil {
		t.Fatalf("a relative directory must be refused")
	}
}

func TestParseProbeIgnoresPluginNoiseBeforeTheAnswer(t *testing.T) {
	payload := []byte("plugin wrote this on stdout\n{\"ok\":true,\"result\":{\"id\":\"x\",\"models\":[\"m\"]}}\n")
	result, errParse := parseProbe(payload)
	if errParse != nil {
		t.Fatalf("parseProbe() error = %v", errParse)
	}
	if result.ID != "x" || len(result.Models) != 1 {
		t.Fatalf("result = %#v", result)
	}
	if _, errBad := parseProbe([]byte("not json")); !errors.Is(errBad, ErrPluginInvalid) {
		t.Fatalf("garbage must be invalid, got %v", errBad)
	}
}
