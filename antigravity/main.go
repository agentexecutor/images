// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Command ax-antigravity runs a Gemini API Managed Agent (Antigravity by default)
// whose tools execute inside the AX task's own sandbox.
//
// Each task creates a self-hosted Gemini API environment, starts
// `gemini-api environments connect` in the workspace to run the agent's tool
// calls (execute_bash, read_file, write_file, edit_file, ...), creates an
// interaction from the task's agent spec, and waits for it to finish. The
// agent loop runs in the Gemini API; only its tools run here.
//
// Inputs provided by the mounted AX runner (see runner/launcher.go in
// github.com/google/ax):
//
//	AX_GOAL_FILE        path to the task's goal (spec.goal.text), sent as the
//	                    interaction's input unless spec.config.input is set
//	AX_CONFIG_FILE      optional path to spec.config as JSON: interaction
//	                    request fields and harness options (see config)
//	AX_MCP_CONFIG_FILE  ignored; Managed Agents call MCP servers from the
//	                    Gemini API, not from the sandbox
//	AX_SESSION_FILE     optional path to spec.sessionId; when non-empty, the
//	                    environment is kept in <workspace>/.ax/sessions/<id>
//	                    and follow-up turns continue the previous interaction
//	AX_OUTPUT_FILE      optional path the agent's final answer is written to
//	                    (AX reports it as status.outcome.output)
//	cwd                 the task's workspace; the tools' root and working dir
//
// Optional environment variables:
//
//	GEMINI_API_KEY          API key (required; may be a placeholder replaced
//	                        by an AX Gateway)
//	GOOGLE_GEMINI_BASE_URL  API base URL (default:
//	                        https://generativelanguage.googleapis.com)
//	AX_SESSION_DIR          override the session directory
//	AX_POLL_INTERVAL        interval between interaction status polls
//	                        (Go duration, default 2s)
//	GEMINI_API_BIN          gemini-api binary (default: gemini-api)
//	AX_CA_BUNDLE            CA bundle path (default:
//	                        /run/ate/egress-trust-bundle/trust-bundle.pem)
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	gi "google.golang.org/genai/interactions/models/interactions"
)

const (
	defaultAgent = "antigravity-preview-09-2026"
	defaultCA    = "/run/ate/egress-trust-bundle/trust-bundle.pem"
	stateFile    = "managed-agent.json"

	maxContinuations = 20
)

// config is spec.config: the fields of a Gemini API CreateInteraction request
// for an agent (agent, previous_interaction_id, system_instruction, tools,
// agent_config, ...), sent as-is, plus the harness's own keys below. Unknown
// keys are rejected.
type config struct {
	// Request holds the interaction request fields.
	Request map[string]json.RawMessage `json:"request,omitempty"`
	// Inputs are text parts sent as the interaction's input, instead of
	// spec.goal.text.
	Inputs []string `json:"inputs,omitempty"`
	// OutputFile is an extra path the answer is written to.
	OutputFile string `json:"outputFile,omitempty"`
	// CommandTimeout bounds each execute_bash command (Go duration, default
	// 2m).
	CommandTimeout string `json:"commandTimeout,omitempty"`
	// DisableBash disables the execute_bash tool.
	DisableBash bool `json:"disableBash,omitempty"`
	// Timeout bounds the wait for the interaction (Go duration; default: no
	// limit).
	Timeout string `json:"timeout,omitempty"`
}

// harnessKeys are the spec.config keys the harness reads itself.
var harnessKeys = []string{"inputs", "outputFile", "commandTimeout", "disableBash", "timeout"}

// reservedFields are interaction request fields the harness sets.
var reservedFields = []string{"environment", "background", "stream", "store", "continuation_token"}

// replacedFields are interaction request fields spec.config sets under
// another key.
var replacedFields = map[string]string{"input": "inputs"}

// deprecatedFields are interaction request fields the Gemini API deprecated.
var deprecatedFields = []string{"response_mime_type", "response_modalities"}

// requestFields maps the camelCase spec.config keys to the CreateInteraction
// fields they set.
var requestFields = func() map[string]string {
	fields := map[string]string{}
	t := reflect.TypeFor[gi.CreateAgentInteraction]()
	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" && !slices.Contains(reservedFields, name) &&
			replacedFields[name] == "" && !slices.Contains(deprecatedFields, name) {
			fields[camelCase(name)] = name
		}
	}
	return fields
}()

// camelCase converts a snake_case name to camelCase.
func camelCase(name string) string {
	parts := strings.Split(name, "_")
	for i := 1; i < len(parts); i++ {
		if parts[i] != "" {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}

// configKeys returns the supported spec.config keys, sorted.
func configKeys() []string {
	keys := slices.Collect(maps.Keys(requestFields))
	keys = append(keys, harnessKeys...)
	slices.Sort(keys)
	return keys
}

var sessionIDRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// errStopped reports that the harness was signalled to stop.
var errStopped = errors.New("stopped")

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	os.Exit(run(ctx, os.Getenv, os.Stdout, os.Stderr))
}

// harness holds one task run.
type harness struct {
	getenv func(string) string
	stdout io.Writer
	stderr io.Writer
}

func (h *harness) logf(format string, args ...any) {
	fmt.Fprintf(h.stderr, "ax-antigravity: "+format+"\n", args...)
}

// run runs the task and returns the process exit code.
func run(ctx context.Context, getenv func(string) string, stdout, stderr io.Writer) int {
	// The worker's output is copied to stderr concurrently with the logs.
	h := &harness{getenv: getenv, stdout: stdout, stderr: &syncWriter{w: stderr}}
	code, err := h.run(ctx)
	if err != nil {
		h.logf("%v", err)
	}
	return code
}

type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

func readFile(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	b, err := os.ReadFile(path)
	return string(b), err
}

func strictUnmarshal(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func loadConfig(path string) (config, error) {
	var cfg config
	raw, err := readFile(path)
	if err != nil || strings.TrimSpace(raw) == "" {
		return cfg, err
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &keys); err != nil {
		return cfg, fmt.Errorf("spec.config: %w", err)
	}
	harness := map[string]json.RawMessage{}
	for k, v := range keys {
		switch {
		case slices.Contains(harnessKeys, k):
			harness[k] = v
		case requestFields[k] != "":
			if cfg.Request == nil {
				cfg.Request = map[string]json.RawMessage{}
			}
			cfg.Request[requestFields[k]] = v
		case requestFields[camelCase(k)] != "":
			return cfg, fmt.Errorf("use spec.config.%s instead of %s", camelCase(k), k)
		case slices.Contains(reservedFields, k) || slices.ContainsFunc(reservedFields, func(f string) bool { return camelCase(f) == k }):
			return cfg, fmt.Errorf("spec.config.%s is set by ax-antigravity", k)
		case slices.Contains(deprecatedFields, k) || slices.ContainsFunc(deprecatedFields, func(f string) bool { return camelCase(f) == k }):
			return cfg, fmt.Errorf("spec.config.%s is deprecated by the Gemini API", k)
		case replacedFields[k] != "":
			return cfg, fmt.Errorf("use spec.config.%s instead of %s", replacedFields[k], k)
		default:
			return cfg, fmt.Errorf("unknown spec.config key %q (supported: %s)", k, strings.Join(configKeys(), ", "))
		}
	}
	b, _ := json.Marshal(harness)
	if err := strictUnmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("spec.config: %w", err)
	}
	for key, val := range map[string]string{"commandTimeout": cfg.CommandTimeout, "timeout": cfg.Timeout} {
		if val == "" {
			continue
		}
		if d, err := time.ParseDuration(val); err != nil || d <= 0 {
			return cfg, fmt.Errorf("spec.config.%s must be a positive Go duration such as 90s or 15m, got %q", key, val)
		}
	}
	if slices.Contains(cfg.Inputs, "") {
		return cfg, errors.New("spec.config.inputs must be a list of non-empty strings")
	}
	if v, ok := cfg.Request["agent"]; ok {
		var name string
		if err := json.Unmarshal(v, &name); err != nil || name == "" {
			return cfg, errors.New("spec.config.agent must be a managed agent name")
		}
	}
	// Check the fields against the SDK's request type now, rather than
	// after creating an environment.
	req := map[string]any{"input": cfg.input("x")}
	for k, v := range cfg.Request {
		req[k] = v
	}
	b, _ = json.Marshal(req)
	if err := json.Unmarshal(b, new(gi.CreateAgentInteraction)); err != nil {
		return cfg, fmt.Errorf("spec.config: %w", err)
	}
	return cfg, nil
}

// input returns the interaction's input: the inputs as text parts, or else
// the goal.
func (cfg config) input(goal string) any {
	if len(cfg.Inputs) == 0 {
		return goal
	}
	parts := make([]map[string]string, len(cfg.Inputs))
	for i, text := range cfg.Inputs {
		parts[i] = map[string]string{"type": "text", "text": text}
	}
	return parts
}

// state is durable per-task (or per-session) state on the workspace volume:
// the environment, the interaction in flight, and the last finished one.
type state struct {
	path string

	Environment       *environment `json:"environment,omitempty"`
	Pending           *pending     `json:"pending,omitempty"`
	LastInteractionID string       `json:"last_interaction_id,omitempty"`
	// Done is the digest of the last input that finished successfully. A
	// harness run as spec.commands is relaunched on every resume, even
	// after it succeeded; a relaunch for the same input then does nothing.
	Done string `json:"done,omitempty"`
}

type environment struct {
	ID  string `json:"id"`
	Key string `json:"key"`
}

// pending is the interaction in flight, and a digest of the goal and config
// that created it, so a relaunched harness reattaches to it.
type pending struct {
	ID     string `json:"id"`
	Digest string `json:"digest"`
}

func loadState(dir string) (*state, error) {
	s := &state{path: filepath.Join(dir, stateFile)}
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, s); err != nil {
		return nil, fmt.Errorf("%s: %w", s.path, err)
	}
	return s, nil
}

// save writes the state with mode 0600, since it holds the environment key.
func (s *state) save() error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return writeAtomic(s.path, b, 0o600)
}

func (s *state) remove() error {
	if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// writeAtomic writes via a temp file and rename, so readers never see a
// partial file.
func writeAtomic(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (h *harness) run(ctx context.Context) (int, error) {
	goalFile := h.getenv("AX_GOAL_FILE")
	if goalFile == "" {
		return 1, errors.New("AX_GOAL_FILE is not set; ax-antigravity must be started by the AX runner")
	}
	goal, err := readFile(goalFile)
	if err != nil {
		return 1, err
	}
	cfg, err := loadConfig(h.getenv("AX_CONFIG_FILE"))
	if err != nil {
		return 1, err
	}
	switch hasInputs := len(cfg.Inputs) > 0; {
	case hasInputs && strings.TrimSpace(goal) != "":
		return 2, errors.New("both spec.goal.text and spec.config.inputs are set; set only one")
	case !hasInputs && strings.TrimSpace(goal) == "":
		return 2, fmt.Errorf("empty goal in %s; set spec.goal.text or spec.config.inputs", goalFile)
	}
	apiKey := h.getenv("GEMINI_API_KEY")
	if apiKey == "" {
		return 1, errors.New("GEMINI_API_KEY is not set")
	}
	pollInterval := 2 * time.Second
	if v := h.getenv("AX_POLL_INTERVAL"); v != "" {
		if pollInterval, err = time.ParseDuration(v); err != nil {
			return 1, fmt.Errorf("AX_POLL_INTERVAL: %w", err)
		}
	}

	sessionFile, err := readFile(h.getenv("AX_SESSION_FILE"))
	if err != nil {
		return 1, err
	}
	sessionID := strings.TrimSpace(sessionFile)
	if sessionID != "" && (!sessionIDRE.MatchString(sessionID) || sessionID == "." || sessionID == "..") {
		return 2, fmt.Errorf("invalid session ID %q", sessionID)
	}
	wd, err := os.Getwd()
	if err != nil {
		return 1, err
	}
	stateDir := filepath.Join(wd, ".ax")
	if sessionID != "" {
		stateDir = h.getenv("AX_SESSION_DIR")
		if stateDir == "" {
			stateDir = filepath.Join(wd, ".ax", "sessions", sessionID)
		}
	}
	st, err := loadState(stateDir)
	if err != nil {
		return 1, err
	}
	h.warnMCP()
	digest := digestOf(goal, cfg)
	if st.Done == digest && st.Pending == nil {
		h.logf("already finished for this input; not running it again")
		return 0, nil
	}

	// Go's crypto/tls reads SSL_CERT_FILE, for this process and the worker.
	caBundle := h.getenv("AX_CA_BUNDLE")
	if caBundle == "" {
		caBundle = defaultCA
	}
	sslCertFile := h.getenv("SSL_CERT_FILE")
	if _, err := os.Stat(caBundle); err == nil && sslCertFile == "" {
		sslCertFile = caBundle
	}

	baseURL := h.getenv("GOOGLE_GEMINI_BASE_URL")
	api, err := newClient(ctx, baseURL, apiKey, sslCertFile)
	if err != nil {
		return 1, err
	}

	if st.Environment == nil {
		env, err := api.createEnvironment(ctx)
		if err != nil {
			return h.stopped(ctx, 1, err)
		}
		st.Environment = env
		if err := st.save(); err != nil {
			return 1, err
		}
		h.logf("created self-hosted environment %s", env.ID)
	}

	request := map[string]any{
		"agent":       defaultAgent,
		"input":       cfg.input(goal),
		"environment": st.Environment.ID,
		"background":  true,
		"store":       true,
		"stream":      false,
	}
	for k, v := range cfg.Request {
		request[k] = v
	}
	// Later turns of a session continue from the session's last interaction.
	if sessionID != "" && st.LastInteractionID != "" {
		request["previous_interaction_id"] = st.LastInteractionID
	}
	var deadline time.Time
	if cfg.Timeout != "" {
		d, _ := time.ParseDuration(cfg.Timeout)
		deadline = time.Now().Add(d)
	}

	w := newWorker(h, st.Environment, cfg, wd, baseURL, sslCertFile)
	if err := w.start(); err != nil {
		return 1, err
	}
	t := &turn{h: h, api: api, worker: w, state: st, digest: digest, deadline: deadline, pollInterval: pollInterval}
	it, err := t.run(ctx, request)
	w.stop()
	if errors.Is(err, errStopped) {
		// AX stops the harness when it suspends the task and relaunches it
		// with the same goal on resume. Leave the interaction running and
		// keep the environment, so the relaunched harness reattaches.
		if p := st.Pending; p != nil {
			h.logf("stopped; leaving interaction %s running for the next launch", p.ID)
		}
		return 143, nil
	}
	if err != nil {
		return 1, err
	}

	st.Pending = nil
	answered := it.Status == "completed" || it.Status == "incomplete" || it.Status == "budget_exceeded"
	if !answered {
		h.finish(api, st, sessionID)
		return 1, errors.New(it.errorText())
	}
	st.LastInteractionID = it.ID

	text := it.answer()
	if text != "" && !strings.HasSuffix(text, "\n") {
		fmt.Fprintln(h.stdout, text)
	} else {
		fmt.Fprint(h.stdout, text)
	}
	outputs := map[string]bool{}
	for _, p := range []string{h.getenv("AX_OUTPUT_FILE"), cfg.OutputFile} {
		if p == "" || outputs[p] {
			continue
		}
		outputs[p] = true
		if err := writeAtomic(p, []byte(text), 0o644); err != nil {
			h.finish(api, st, sessionID)
			return 1, err
		}
	}
	if it.Status != "completed" {
		h.finish(api, st, sessionID)
		return 1, fmt.Errorf("interaction %s ended with status %s", it.ID, it.Status)
	}
	st.Done = digest
	h.finish(api, st, sessionID)
	return 0, nil
}

// stopped returns 143 when ctx was cancelled by a signal, and code otherwise.
func (h *harness) stopped(ctx context.Context, code int, err error) (int, error) {
	if ctx.Err() != nil {
		return 143, nil
	}
	return code, err
}

func (h *harness) warnMCP() {
	raw, err := readFile(h.getenv("AX_MCP_CONFIG_FILE"))
	if err != nil || strings.TrimSpace(raw) == "" {
		return
	}
	var mcp struct {
		MCPServers map[string]any `json:"mcpServers"`
	}
	if json.Unmarshal([]byte(raw), &mcp) == nil && len(mcp.MCPServers) > 0 {
		h.logf("ignoring Workspace MCP servers; Managed Agents do not reach MCP servers through the sandbox")
	}
}

// finish keeps a session's environment for its next turn, and deletes a
// one-off task's environment.
func (h *harness) finish(api *client, st *state, sessionID string) {
	if sessionID != "" {
		if err := st.save(); err != nil {
			h.logf("saving session state: %v", err)
		}
		return
	}
	if err := api.deleteEnvironment(context.Background(), st.Environment.ID); err != nil {
		h.logf("failed to delete environment %s: %v", st.Environment.ID, err)
	}
	if st.Done == "" {
		if err := st.remove(); err != nil {
			h.logf("%v", err)
		}
		return
	}
	// Keep only the record that this input finished.
	done := &state{path: st.path, Done: st.Done}
	if err := done.save(); err != nil {
		h.logf("%v", err)
	}
}

func digestOf(goal string, cfg config) string {
	b, _ := json.Marshal([]any{goal, cfg})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// turn runs one interaction to completion.
type turn struct {
	h            *harness
	api          *client
	worker       *worker
	state        *state
	digest       string
	deadline     time.Time
	pollInterval time.Duration
}

// run creates the interaction (or reattaches to one a relaunched harness left
// in flight), follows continuations, and returns the final interaction.
func (t *turn) run(ctx context.Context, request map[string]any) (*interaction, error) {
	var id string
	if p := t.state.Pending; p != nil && p.Digest == t.digest {
		id = p.ID
		t.h.logf("reattaching to interaction %s", id)
	} else {
		var err error
		if id, err = t.create(ctx, request); err != nil {
			return nil, err
		}
		t.h.logf("created interaction %s", id)
	}
	for range maxContinuations {
		it, err := t.wait(ctx, id)
		if err != nil {
			return nil, err
		}
		switch it.Status {
		case "requires_action":
			if err := t.api.cancel(ctx, id); err != nil {
				t.h.logf("%v", err)
			}
			return nil, errors.New("the agent called a client-side function tool, which ax-antigravity cannot answer")
		case "requires_continuation":
			follow := map[string]any{}
			for k, v := range request {
				if k != "input" {
					follow[k] = v
				}
			}
			follow["previous_interaction_id"] = id
			follow["continuation_token"] = it.ContinuationToken
			if id, err = t.create(ctx, follow); err != nil {
				return nil, err
			}
			t.h.logf("continuing in interaction %s", id)
		default:
			return it, nil
		}
	}
	return nil, fmt.Errorf("interaction did not finish after %d continuations", maxContinuations)
}

func (t *turn) create(ctx context.Context, request map[string]any) (string, error) {
	if ctx.Err() != nil {
		return "", errStopped
	}
	// A stop must not abort the call: the server may have created the
	// interaction already, and its ID is needed to reattach on relaunch.
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	id, err := t.api.createInteraction(cctx, request)
	if err != nil {
		return "", err
	}
	t.state.Pending = &pending{ID: id, Digest: t.digest}
	if err := t.state.save(); err != nil {
		return "", err
	}
	if ctx.Err() != nil {
		return "", errStopped
	}
	return id, nil
}

func (t *turn) wait(ctx context.Context, id string) (*interaction, error) {
	for {
		it, err := t.api.getInteraction(ctx, id)
		if ctx.Err() != nil {
			return nil, errStopped
		}
		if err != nil {
			return nil, err
		}
		switch it.Status {
		case "completed", "failed", "cancelled", "incomplete", "budget_exceeded",
			"requires_action", "requires_continuation":
			return it, nil
		}
		if !t.deadline.IsZero() && time.Now().After(t.deadline) {
			return nil, fmt.Errorf("interaction %s did not finish within spec.config.timeout", id)
		}
		if err := t.worker.check(); err != nil {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, errStopped
		case <-time.After(t.pollInterval):
		}
	}
}
