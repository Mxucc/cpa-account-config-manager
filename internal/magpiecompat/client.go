// Package magpiecompat runs a Magpie/OpenCode provider plugin in a short-lived
// Bun sidecar and reads three hooks from it: the login methods it declares, its
// model catalog and its allowance windows.
//
// The sidecar never proxies requests: a plugin's fetch hook is not called, so
// routing stays with the host (CLIProxyAPI). Importing and listing a plugin's
// models is safe to run on any machine with Bun; without Bun the caller gets
// ErrBunUnavailable and keeps the manifest-only behaviour.
//
// Isolation rules, all enforced here rather than by the plugin:
//   - one process per probe, so a crash leaks nothing into the next call;
//   - a hard timeout that kills the process group;
//   - a bounded stdout read, so a plugin cannot exhaust plugin memory;
//   - a concurrency cap, so a slow plugin cannot queue up processes;
//   - an executable taken from an explicit allow-list (bun only).
package magpiecompat

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	// ErrBunUnavailable means no allowed runtime was found on this machine.
	ErrBunUnavailable = errors.New("bun is not available; plugin hooks were not executed")
	// ErrPluginInvalid means the plugin did not answer with a usable probe.
	ErrPluginInvalid = errors.New("plugin did not answer the probe")
	// ErrOutputTooLarge means the plugin wrote more than the allowed stdout.
	ErrOutputTooLarge = errors.New("plugin output exceeded the size limit")
	// ErrTimeout means the plugin did not finish within the deadline.
	ErrTimeout = errors.New("plugin probe timed out")
)

// ProbeResult is what the sidecar reports for one plugin.
type ProbeResult struct {
	ID       string   `json:"id"`
	Methods  []string `json:"methods,omitempty"`
	Models   []string `json:"models,omitempty"`
	Windows  []Window `json:"windows,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// Window is one allowance window as the plugin reported it. Used and Balance are
// pointers so "not reported" stays distinguishable from zero.
type Window struct {
	Name    string   `json:"name"`
	Used    *float64 `json:"used,omitempty"`
	Limit   *float64 `json:"limit,omitempty"`
	Balance *float64 `json:"balance,omitempty"`
	Unit    string   `json:"unit,omitempty"`
	Reset   string   `json:"reset,omitempty"`
	Aside   bool     `json:"aside,omitempty"`
}

// Options bounds one runner. Zero values fall back to the defaults below.
type Options struct {
	// BunPath is the runtime executable. Empty means "resolve from PATH, bun only".
	BunPath string
	// HostScript overrides the sidecar script (tests use a fixture).
	HostScript    string
	Timeout       time.Duration
	MaxOutput     int64
	MaxConcurrent int
}

const (
	defaultTimeout       = 20 * time.Second
	defaultMaxOutput     = 4 << 20
	defaultMaxConcurrent = 2
	// allowedBunNames is the executable allow-list. A manifest cannot choose it.
	allowedBunNames = "bun"
)

// Runner executes plugin probes. It is safe for concurrent use.
type Runner struct {
	bunPath  string
	script   string
	timeout  time.Duration
	maxBytes int64
	sem      chan struct{}
	// commandFactory is replaced by tests to simulate a timeout, a crash or a
	// flood of output without needing a real plugin.
	commandFactory func(ctx context.Context, name string, args ...string) *exec.Cmd
}

// New builds a runner. bunPath may be empty, in which case PATH is searched.
func New(bunPath, script string, options Options) *Runner {
	if strings.TrimSpace(bunPath) == "" {
		bunPath = resolveBun()
	}
	if strings.TrimSpace(script) == "" {
		script = sidecarScript()
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	maxBytes := options.MaxOutput
	if maxBytes <= 0 {
		maxBytes = defaultMaxOutput
	}
	concurrent := options.MaxConcurrent
	if concurrent <= 0 {
		concurrent = defaultMaxConcurrent
	}
	return &Runner{
		bunPath:  bunPath,
		script:   script,
		timeout:  timeout,
		maxBytes: maxBytes,
		sem:      make(chan struct{}, concurrent),
		commandFactory: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			return exec.CommandContext(ctx, name, args...)
		},
	}
}

func resolveBun() string {
	path, errLook := exec.LookPath(allowedBunNames)
	if errLook != nil {
		return ""
	}
	return path
}

// Available reports whether a runtime was resolved.
func (r *Runner) Available() bool {
	return r != nil && r.bunPath != ""
}

// Available is the package-level form used before a runner exists.
func BunAvailable() bool { return resolveBun() != "" }

// Probe runs the sidecar once for the plugin directory and returns its result.
func (r *Runner) Probe(ctx context.Context, directory string) (ProbeResult, error) {
	if !r.Available() {
		return ProbeResult{}, ErrBunUnavailable
	}
	dir := strings.TrimSpace(directory)
	if dir == "" {
		return ProbeResult{}, fmt.Errorf("plugin directory is required")
	}
	if !filepath.IsAbs(dir) {
		return ProbeResult{}, fmt.Errorf("plugin directory must be absolute")
	}

	select {
	case r.sem <- struct{}{}:
		defer func() { <-r.sem }()
	case <-ctx.Done():
		return ProbeResult{}, ctx.Err()
	}

	probeCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	args := []string{r.script, dir}
	command := r.commandFactory(probeCtx, r.bunPath, args...)
	stdout, errPipe := command.StdoutPipe()
	if errPipe != nil {
		return ProbeResult{}, errPipe
	}
	// stderr is drained but never surfaced: plugin output can carry credentials.
	command.Stderr = nil
	if errStart := command.Start(); errStart != nil {
		return ProbeResult{}, fmt.Errorf("start plugin sidecar: %w", errStart)
	}

	type readResult struct {
		payload []byte
		err     error
		tooBig  bool
	}
	read := make(chan readResult, 1)
	go func() {
		reader := bufio.NewReader(stdout)
		buffer := make([]byte, 0, 64<<10)
		for {
			line, errRead := reader.ReadBytes('\n')
			if len(line) > 0 {
				if int64(len(buffer)+len(line)) > r.maxBytes {
					read <- readResult{tooBig: true}
					return
				}
				buffer = append(buffer, line...)
			}
			if errRead != nil {
				if errors.Is(errRead, io.EOF) {
					read <- readResult{payload: buffer}
					return
				}
				read <- readResult{err: errRead}
				return
			}
		}
	}()

	waited := make(chan error, 1)
	go func() { waited <- command.Wait() }()

	var outcome readResult
	select {
	case outcome = <-read:
	case <-probeCtx.Done():
		_ = command.Process.Kill()
		<-waited
		return ProbeResult{}, ErrTimeout
	}

	if outcome.tooBig {
		_ = command.Process.Kill()
		<-waited
		return ProbeResult{}, ErrOutputTooLarge
	}
	if errWait := <-waited; errWait != nil && len(outcome.payload) == 0 {
		// A crashed plugin is isolated: the caller records the reason and moves on.
		return ProbeResult{}, fmt.Errorf("%w: sidecar exited: %v", ErrPluginInvalid, errWait)
	}
	if outcome.err != nil {
		return ProbeResult{}, fmt.Errorf("%w: %v", ErrPluginInvalid, outcome.err)
	}
	if probeCtx.Err() == context.DeadlineExceeded {
		return ProbeResult{}, ErrTimeout
	}

	result, errParse := parseProbe(outcome.payload)
	if errParse != nil {
		return ProbeResult{}, errParse
	}
	return result, nil
}

func parseProbe(payload []byte) (ProbeResult, error) {
	line := strings.TrimSpace(string(payload))
	if line == "" {
		return ProbeResult{}, fmt.Errorf("%w: empty response", ErrPluginInvalid)
	}
	// The sidecar writes one JSON object on the last line; anything the plugin
	// printed before it is ignored rather than trusted.
	parts := strings.Split(line, "\n")
	last := strings.TrimSpace(parts[len(parts)-1])
	var envelope struct {
		OK     bool        `json:"ok"`
		Result ProbeResult `json:"result"`
		Error  string      `json:"error"`
	}
	if errUnmarshal := json.Unmarshal([]byte(last), &envelope); errUnmarshal != nil {
		return ProbeResult{}, fmt.Errorf("%w: %v", ErrPluginInvalid, errUnmarshal)
	}
	if !envelope.OK {
		message := envelope.Error
		if message == "" {
			message = "no reason given"
		}
		return ProbeResult{}, fmt.Errorf("%w: %s", ErrPluginInvalid, message)
	}
	result := envelope.Result
	for _, window := range result.Windows {
		if window.Name == "" {
			return ProbeResult{}, fmt.Errorf("%w: window without a name", ErrPluginInvalid)
		}
	}
	return result, nil
}

// ProbeWith is the package-level convenience used by the manager: it resolves
// Bun, applies the defaults and probes once.
func ProbeWith(ctx context.Context, directory string) (ProbeResult, error) {
	return New("", "", Options{}).Probe(ctx, directory)
}

//go:embed host.mjs
var hostScript []byte

var (
	scriptOnce sync.Once
	scriptPath string
	scriptErr  error
)

// sidecarScript materialises the embedded sidecar once per process. Writing it to
// the temp directory keeps the plugin library free of extra files, and a stable
// path avoids re-writing it for every probe.
func sidecarScript() string {
	scriptOnce.Do(func() {
		path := filepath.Join(os.TempDir(), "cpa-magpiecompat-host-v1.mjs")
		if errWrite := os.WriteFile(path, hostScript, 0o600); errWrite != nil {
			scriptErr = errWrite
			return
		}
		scriptPath = path
	})
	if scriptErr != nil {
		return ""
	}
	return scriptPath
}
