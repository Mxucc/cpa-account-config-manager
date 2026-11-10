package magpiecompat

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Running a community plugin's own sign-in.
//
// The market import used to record a manifest only, so a community provider could
// never be signed in from this page and the dialog told the operator to go do it
// somewhere else. The plugin now downloads the package and runs its sign-in here.
//
// One process per sign-in, because a plugin's authorize() returns a closure that
// polls the flow it started: a second process could not finish it. The process
// writes its progress to a status file, which the host polls, and the credential
// arrives in that file rather than on stdout.

//go:embed login.mjs
var loginScript []byte

var (
	loginOnce sync.Once
	loginPath string
	loginErr  error
)

// LoginMethod is one sign-in method a plugin declares.
type LoginMethod struct {
	ID          string     `json:"id"`
	Index       int        `json:"index"`
	Type        string     `json:"type"`
	Label       string     `json:"label"`
	Interactive bool       `json:"interactive"`
	Form        []LoginArg `json:"form,omitempty"`
}

// LoginArg is one input a method asks for before it can start.
type LoginArg struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Kind     string   `json:"kind"`
	Required bool     `json:"required"`
	Options  []string `json:"options,omitempty"`
}

// LoginMethods is what a plugin declares it can do.
type LoginMethods struct {
	Provider string        `json:"provider"`
	Name     string        `json:"name"`
	Methods  []LoginMethod `json:"methods"`
}

// LoginCredential is what a completed sign-in produced. It is a secret: it is
// handed to the caller that asked for the sign-in and never logged.
type LoginCredential struct {
	Access    string         `json:"access"`
	Refresh   string         `json:"refresh"`
	Expires   int64          `json:"expires"`
	AccountID string         `json:"account_id"`
	State     map[string]any `json:"state,omitempty"`
	Method    string         `json:"method"`
}

// LoginStatus is the state of one running sign-in.
type LoginStatus struct {
	Phase        string           `json:"phase"`
	Provider     string           `json:"provider,omitempty"`
	Method       string           `json:"method,omitempty"`
	Mode         string           `json:"mode,omitempty"`
	URL          string           `json:"url,omitempty"`
	BaseURL      string           `json:"base_url,omitempty"`
	Instructions string           `json:"instructions,omitempty"`
	Message      string           `json:"message,omitempty"`
	Credential   *LoginCredential `json:"credential,omitempty"`
}

// loginSlots bounds how many plugin sign-in processes run at once: each one holds
// a browser flow open until the operator finishes it.
var loginSlots = make(chan struct{}, 2)

// LoginSession is one sign-in the host started.
type LoginSession struct {
	mu         sync.Mutex
	directory  string
	statusPath string
	method     string
	startedAt  time.Time
	cancel     context.CancelFunc
	finished   bool
	last       LoginStatus
}

// Methods reads the sign-in methods a plugin declares. It runs the sidecar once.
func (r *Runner) Methods(ctx context.Context, directory string) (LoginMethods, error) {
	result, errRun := r.runLogin(ctx, directory, "methods", nil, 30*time.Second)
	if errRun != nil {
		return LoginMethods{}, errRun
	}
	var methods LoginMethods
	if errDecode := json.Unmarshal(result, &methods); errDecode != nil {
		return LoginMethods{}, fmt.Errorf("%w: the plugin answered no usable method list", ErrPluginInvalid)
	}
	return methods, nil
}

// AccountModels is what a plugin answers for the account a credential belongs to.
// Models is empty with a Reason when the plugin cannot name them, which is different
// from a provider that serves nothing.
type AccountModels struct {
	Provider string   `json:"provider"`
	Models   []string `json:"models"`
	Reason   string   `json:"reason,omitempty"`
}

// Models asks a plugin for the signed-in account's own model catalog. The package's
// static list is the default for when nobody is signed in; the account's real plan is
// only knowable with the credential.
func (r *Runner) Models(ctx context.Context, directory string, credential LoginCredential) (AccountModels, error) {
	if !r.Available() {
		return AccountModels{}, ErrBunUnavailable
	}
	argument := map[string]any{
		"credential": map[string]any{
			"access":     credential.Access,
			"refresh":    credential.Refresh,
			"expires":    credential.Expires,
			"account_id": credential.AccountID,
			"state":      credential.State,
		},
	}
	payload, errMarshal := json.Marshal(argument)
	if errMarshal != nil {
		return AccountModels{}, fmt.Errorf("the model request could not be encoded")
	}
	result, errRun := r.runLogin(ctx, directory, "models", payload, 45*time.Second)
	if errRun != nil {
		return AccountModels{}, errRun
	}
	var answer AccountModels
	if errDecode := json.Unmarshal(result, &answer); errDecode != nil {
		return AccountModels{}, fmt.Errorf("%w: the plugin answered no usable model list", ErrPluginInvalid)
	}
	return answer, nil
}

// LoginMethods exposes a plugin's declared methods to the caller, which needs them
// to decide between a browser flow and a pasted key.
func (s *LoginSession) Method() string { return s.method }

// StartLogin launches one sign-in and returns the session the caller polls. The
// URL is read from the status file before this returns, so the page can show it
// immediately.
func (r *Runner) StartLogin(ctx context.Context, directory, method string, inputs map[string]string, statusDir string) (*LoginSession, LoginStatus, error) {
	if !r.Available() {
		return nil, LoginStatus{}, ErrBunUnavailable
	}
	if strings.TrimSpace(statusDir) == "" {
		return nil, LoginStatus{}, fmt.Errorf("a private status directory is required")
	}
	if errMkdir := os.MkdirAll(statusDir, 0o700); errMkdir != nil {
		return nil, LoginStatus{}, fmt.Errorf("create the sign-in directory: %w", errMkdir)
	}
	statusPath := filepath.Join(statusDir, fmt.Sprintf("login-%d.json", time.Now().UnixNano()))
	sessionContext, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	payload := map[string]any{
		"status_file": statusPath,
		"method":      method,
		"inputs":      inputs,
	}
	raw, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		cancel()
		return nil, LoginStatus{}, fmt.Errorf("encode the sign-in request: %w", errMarshal)
	}
	session := &LoginSession{
		directory:  directory,
		statusPath: statusPath,
		method:     method,
		startedAt:  time.Now(),
		cancel:     cancel,
	}
	go session.run(r, sessionContext, raw)
	// The plugin needs a moment to open its flow; waiting for the first status
	// keeps the page from rendering an empty dialog.
	status := session.waitForFirst(25 * time.Second)
	return session, status, nil
}

// run executes the sidecar and waits for it, then records the terminal state.
func (s *LoginSession) run(runner *Runner, ctx context.Context, payload []byte) {
	defer s.cancel()
	select {
	case loginSlots <- struct{}{}:
		defer func() { <-loginSlots }()
	case <-ctx.Done():
		s.record(LoginStatus{Phase: "failed", Message: "the sign-in could not start"})
		return
	}
	_, errRun := runner.runLogin(ctx, s.directory, "run", payload, 4*time.Minute)
	if errRun != nil {
		s.record(LoginStatus{Phase: "failed", Message: errRun.Error()})
	}
}

// waitForFirst waits for the plugin to report its flow, or reports a failure.
func (s *LoginSession) waitForFirst(timeout time.Duration) LoginStatus {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status, ok := s.read()
		if ok {
			return status
		}
		time.Sleep(150 * time.Millisecond)
	}
	return LoginStatus{Phase: "failed", Message: "the plugin did not open a sign-in page in time"}
}

// Poll returns the current state of the sign-in.
func (s *LoginSession) Poll() LoginStatus {
	if status, ok := s.read(); ok {
		return status
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last.Phase != "" {
		return s.last
	}
	return LoginStatus{Phase: "waiting"}
}

// Close stops a sign-in that is still running.
func (s *LoginSession) Close() {
	s.mu.Lock()
	finished := s.finished
	s.finished = true
	s.mu.Unlock()
	if !finished {
		s.cancel()
	}
	_ = os.Remove(s.statusPath)
}

func (s *LoginSession) read() (LoginStatus, bool) {
	raw, errRead := os.ReadFile(s.statusPath)
	if errRead != nil {
		return LoginStatus{}, false
	}
	var status LoginStatus
	if errUnmarshal := json.Unmarshal(raw, &status); errUnmarshal != nil {
		return LoginStatus{}, false
	}
	if status.Phase == "" {
		return LoginStatus{}, false
	}
	if status.Phase == "success" || status.Phase == "failed" {
		s.record(status)
	}
	return status, true
}

func (s *LoginSession) record(status LoginStatus) {
	s.mu.Lock()
	s.last = status
	if status.Phase == "success" || status.Phase == "failed" {
		s.finished = true
	}
	s.mu.Unlock()
}

// runLogin executes the sidecar once and returns its single JSON result line.
func (r *Runner) runLogin(ctx context.Context, directory, action string, payload []byte, timeout time.Duration) ([]byte, error) {
	if !r.Available() {
		return nil, ErrBunUnavailable
	}
	dir := strings.TrimSpace(directory)
	if dir == "" || !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("plugin directory must be an absolute path")
	}
	script, errScript := loginScriptPath()
	if errScript != nil {
		return nil, errScript
	}
	arguments := []string{script, dir, action}
	if len(payload) > 0 {
		arguments = append(arguments, string(payload))
	}
	runContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(runContext, r.bunPath, arguments...)
	command.Dir = dir
	command.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"NO_COLOR=1",
	}
	output, errRun := command.Output()
	if errRun != nil {
		if runContext.Err() != nil {
			return nil, ErrTimeout
		}
		return nil, fmt.Errorf("the sign-in process failed: %w", errRun)
	}
	line := lastJSONLine(output)
	if len(line) == 0 {
		return nil, fmt.Errorf("%w: the sign-in process printed nothing", ErrPluginInvalid)
	}
	var envelope struct {
		OK     bool            `json:"ok"`
		Error  string          `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	if errUnmarshal := json.Unmarshal(line, &envelope); errUnmarshal != nil {
		return nil, fmt.Errorf("%w: the sign-in process printed no JSON", ErrPluginInvalid)
	}
	if !envelope.OK {
		message := strings.TrimSpace(envelope.Error)
		if message == "" {
			message = "the plugin refused the sign-in"
		}
		return nil, fmt.Errorf("%s", message)
	}
	return envelope.Result, nil
}

// loginScriptPath materialises the embedded sign-in sidecar once per process.
func loginScriptPath() (string, error) {
	loginOnce.Do(func() {
		path := filepath.Join(os.TempDir(), "cpa-magpiecompat-login-v1.mjs")
		if errWrite := os.WriteFile(path, loginScript, 0o600); errWrite != nil {
			loginErr = errWrite
			return
		}
		loginPath = path
	})
	if loginErr != nil {
		return "", fmt.Errorf("prepare the sign-in runner: %w", loginErr)
	}
	return loginPath, nil
}

// lastJSONLine returns the last line that looks like JSON.
func lastJSONLine(output []byte) []byte {
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		candidate := strings.TrimSpace(lines[index])
		if strings.HasPrefix(candidate, "{") && strings.HasSuffix(candidate, "}") {
			return []byte(candidate)
		}
	}
	return nil
}
