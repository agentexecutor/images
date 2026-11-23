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

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestMain doubles as the fake `gemini-api environments connect`: when
// FAKE_WORKER_DIR is set, the test binary records its args and environment,
// marks its environment as leased, and runs until terminated.
func TestMain(m *testing.M) {
	if dir := os.Getenv("FAKE_WORKER_DIR"); dir != "" {
		fakeWorker(dir)
		return
	}
	os.Exit(m.Run())
}

type workerRecord struct {
	Args   []string `json:"args"`
	Cwd    string   `json:"cwd"`
	EnvKey string   `json:"env_key"`
	APIKey string   `json:"api_key"`
}

func fakeWorker(dir string) {
	cwd, _ := os.Getwd()
	b, _ := json.Marshal(workerRecord{
		Args:   os.Args[1:],
		Cwd:    cwd,
		EnvKey: os.Getenv("GEMINI_ENVIRONMENT_KEY"),
		APIKey: os.Getenv("GEMINI_API_KEY"),
	})
	f, _ := os.OpenFile(filepath.Join(dir, "worker.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	f.Write(append(b, '\n'))
	f.Close()
	if os.Getenv("FAKE_WORKER_CRASH") != "" {
		os.Exit(7)
	}
	os.WriteFile(filepath.Join(dir, "leased-"+os.Getenv("GEMINI_ENVIRONMENT_KEY")), nil, 0o644)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM)
	<-sig
	os.Exit(0)
}

type logEntry struct {
	Method string
	Path   string
	Body   map[string]any
}

type fakeInteraction struct {
	req    map[string]any
	env    string
	status string
	polls  int
}

// fakeAPI is a fake Gemini API. An interaction completes once the fake worker
// has leased its environment. Its outcome comes from the input: "status=<s>"
// ends with that status, and "continue" first returns requires_continuation.
type fakeAPI struct {
	t         *testing.T
	workerDir string

	mu    sync.Mutex
	log   []logEntry
	envs  map[string]string
	inter map[string]*fakeInteraction
	nEnv  int
	hold  bool // keep interactions in progress
}

func newFakeAPI(t *testing.T, workerDir string) (*fakeAPI, *httptest.Server) {
	f := &fakeAPI{t: t, workerDir: workerDir, envs: map[string]string{}, inter: map[string]*fakeInteraction{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeAPI) entries() []logEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]logEntry(nil), f.log...)
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)
	f.log = append(f.log, logEntry{r.Method, r.URL.Path, body})
	reply := func(code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(v)
	}
	if r.Header.Get("x-goog-api-key") != "test-key" {
		reply(401, map[string]any{"error": map[string]any{"message": "bad key"}})
		return
	}
	p := strings.TrimPrefix(r.URL.Path, "/v1beta")
	last := p[strings.LastIndex(p, "/")+1:]
	switch {
	case r.Method == "POST" && p == "/environments":
		f.nEnv++
		id := fmt.Sprintf("env%d", f.nEnv)
		f.envs[id] = "ek_" + id
		reply(200, map[string]any{"id": "environments/" + id, "environment_key": f.envs[id]})
	case r.Method == "DELETE" && strings.HasPrefix(p, "/environments/"):
		delete(f.envs, last)
		reply(200, map[string]any{})
	case r.Method == "POST" && p == "/interactions":
		env, _ := body["environment"].(string)
		if _, ok := f.envs[env]; !ok {
			reply(404, map[string]any{"error": map[string]any{"message": "no environment " + env}})
			return
		}
		id := fmt.Sprintf("int%d", len(f.inter)+1)
		f.inter[id] = &fakeInteraction{req: body, env: env, status: "in_progress"}
		reply(200, map[string]any{"id": id, "status": "in_progress"})
	case r.Method == "POST" && strings.HasSuffix(p, "/cancel"):
		f.inter[strings.Split(p, "/")[2]].status = "cancelled"
		reply(200, map[string]any{})
	case r.Method == "GET" && strings.HasPrefix(p, "/interactions/"):
		it := f.inter[last]
		input, _ := it.req["input"].(string)
		it.polls++
		_, err := os.Stat(filepath.Join(f.workerDir, "leased-"+f.envs[it.env]))
		if it.status == "in_progress" && err == nil && it.polls >= 2 && !f.hold {
			_, continued := it.req["continuation_token"]
			switch {
			case strings.Contains(input, "continue") && !continued:
				it.status = "requires_continuation"
			case strings.Contains(input, "status="):
				it.status = strings.Fields(strings.SplitN(input, "status=", 2)[1])[0]
			default:
				it.status = "completed"
			}
		}
		out := map[string]any{"id": last, "status": it.status}
		switch it.status {
		case "requires_continuation":
			out["continuation_token"] = "tok1"
		case "completed", "incomplete":
			out["steps"] = []any{
				map[string]any{"type": "user_input", "content": []any{map[string]any{"type": "text", "text": input}}},
				map[string]any{"type": "model_output", "content": []any{map[string]any{"type": "text", "text": "ANSWER(" + last + ")"}}},
			}
		case "failed":
			out["errors"] = []any{map[string]any{"message": "agent exploded"}}
		}
		reply(200, out)
	default:
		reply(404, map[string]any{"error": map[string]any{"message": r.URL.Path}})
	}
}

// env is a test's harness setup: a workspace, a fake API, and the fake worker.
type env struct {
	t         *testing.T
	ws        string
	runDir    string
	workerDir string
	api       *fakeAPI
	vars      map[string]string
}

func newEnv(t *testing.T) *env {
	dir := t.TempDir()
	e := &env{t: t, ws: filepath.Join(dir, "ws"), runDir: filepath.Join(dir, "run"), workerDir: filepath.Join(dir, "worker")}
	for _, d := range []string{e.ws, e.runDir, e.workerDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	api, srv := newFakeAPI(t, e.workerDir)
	e.api = api
	e.vars = map[string]string{
		"GEMINI_API_KEY":         "test-key",
		"GOOGLE_GEMINI_BASE_URL": srv.URL,
		"GEMINI_API_BIN":         os.Args[0],
		"AX_POLL_INTERVAL":       "10ms",
		"AX_GOAL_FILE":           filepath.Join(e.runDir, "goal.md"),
		"AX_CONFIG_FILE":         filepath.Join(e.runDir, "config.json"),
		"AX_OUTPUT_FILE":         filepath.Join(e.ws, ".ax", "output"),
	}
	// The worker inherits the process environment.
	t.Setenv("FAKE_WORKER_DIR", e.workerDir)
	t.Setenv("GEMINI_API_KEY", "test-key")
	t.Chdir(e.ws)
	return e
}

// run runs the harness with goal and config and returns its exit code,
// stdout and stderr. extra overrides harness variables.
func (e *env) run(goal, cfg string, extra ...string) (int, string, string) {
	e.t.Helper()
	os.WriteFile(e.vars["AX_GOAL_FILE"], []byte(goal), 0o644)
	os.WriteFile(e.vars["AX_CONFIG_FILE"], []byte(cfg), 0o644)
	os.Remove(filepath.Join(e.workerDir, "worker.log"))
	vars := map[string]string{}
	for k, v := range e.vars {
		vars[k] = v
	}
	for _, kv := range extra {
		k, v, _ := strings.Cut(kv, "=")
		vars[k] = v
	}
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), func(k string) string { return vars[k] }, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func (e *env) workers() []workerRecord {
	b, _ := os.ReadFile(filepath.Join(e.workerDir, "worker.log"))
	var recs []workerRecord
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var r workerRecord
		json.Unmarshal([]byte(line), &r)
		recs = append(recs, r)
	}
	return recs
}

func interactionPosts(log []logEntry) []map[string]any {
	var out []map[string]any
	for _, l := range log {
		if l.Method == "POST" && l.Path == "/v1beta/interactions" {
			out = append(out, l.Body)
		}
	}
	return out
}

func readOrEmpty(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

func TestDefaults(t *testing.T) {
	e := newEnv(t)
	code, out, stderr := e.run("Create hello.py.", "{}")
	if code != 0 || out != "ANSWER(int1)\n" {
		t.Fatalf("code=%d stdout=%q stderr=%s", code, out, stderr)
	}
	if got := readOrEmpty(filepath.Join(e.ws, ".ax", "output")); got != "ANSWER(int1)" {
		t.Errorf("AX_OUTPUT_FILE=%q", got)
	}
	log := e.api.entries()
	if log[0].Path != "/v1beta/environments" || !reflect.DeepEqual(log[0].Body, map[string]any{"mode": "self_hosted"}) {
		t.Errorf("first call %+v, want POST /environments {mode: self_hosted}", log[0])
	}
	want := map[string]any{
		"agent": "antigravity-preview-09-2026", "input": "Create hello.py.", "environment": "env1",
		"background": true, "store": true, "stream": false,
		"agent_config": map[string]any{"type": "antigravity"},
	}
	if got := interactionPosts(log)[0]; !reflect.DeepEqual(got, want) {
		t.Errorf("interaction request\n got  %v\n want %v", got, want)
	}
	if l := log[len(log)-1]; l.Method != "DELETE" || l.Path != "/v1beta/environments/env1" {
		t.Errorf("last call %s %s, want DELETE /v1beta/environments/env1", l.Method, l.Path)
	}
	w := e.workers()[0]
	if w.EnvKey != "ek_env1" || w.APIKey != "" {
		t.Errorf("worker env key %q api key %q; want ek_env1 and no API key", w.EnvKey, w.APIKey)
	}
	for _, a := range []string{"environments", "connect", "--root-dir=" + w.Cwd, "--workdir=" + w.Cwd, "--max-concurrent=1", "--server-url=" + e.vars["GOOGLE_GEMINI_BASE_URL"]} {
		if !contains(w.Args, a) {
			t.Errorf("worker args %v missing %s", w.Args, a)
		}
	}
	st, err := loadState(filepath.Join(e.ws, ".ax"))
	if err != nil || st.Environment != nil || st.Pending != nil || st.Done == "" {
		t.Errorf("state after a one-off task: %+v, %v; want only the done record", st, err)
	}

	// A relaunch for the same input, as after a resume, does nothing.
	n := len(e.api.entries())
	if code, _, stderr := e.run("Create hello.py.", "{}"); code != 0 || !strings.Contains(stderr, "already finished") {
		t.Fatalf("relaunch: code=%d stderr=%s", code, stderr)
	}
	if got := len(e.api.entries()); got != n {
		t.Errorf("relaunch made %d API calls", got-n)
	}
	// A new input in the same workspace runs.
	if code, _, stderr := e.run("Say bye.", "{}"); code != 0 || len(interactionPosts(e.api.entries())) != 2 {
		t.Fatalf("new input: code=%d stderr=%s", code, stderr)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func TestRequestFields(t *testing.T) {
	e := newEnv(t)
	cfg := `{"agent":"antigravity-preview-05-2026","systemInstruction":"Be terse.",
	  "tools":[{"type":"google_search"}],"agentConfig":{"model":"gemini-x"},
	  "previousInteractionId":"prev-1","labels":{"team":"ax"},
	  "commandTimeout":"5m","disableBash":true,"outputFile":"out/answer.txt"}`
	if code, _, stderr := e.run("Do it.", cfg); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	p := interactionPosts(e.api.entries())[0]
	if p["agent"] != "antigravity-preview-05-2026" || p["system_instruction"] != "Be terse." ||
		p["input"] != "Do it." || p["previous_interaction_id"] != "prev-1" {
		t.Errorf("request %v", p)
	}
	if !reflect.DeepEqual(p["tools"], []any{map[string]any{"type": "google_search"}}) ||
		!reflect.DeepEqual(p["agent_config"], map[string]any{"type": "antigravity", "model": "gemini-x"}) ||
		!reflect.DeepEqual(p["labels"], map[string]any{"team": "ax"}) {
		t.Errorf("tools/agent_config/labels %v %v %v", p["tools"], p["agent_config"], p["labels"])
	}
	for _, k := range []string{"commandTimeout", "disableBash", "outputFile"} {
		if _, ok := p[k]; ok {
			t.Errorf("harness key %s sent in the request", k)
		}
	}
	w := e.workers()[0]
	if !contains(w.Args, "--command-timeout=5m") || !contains(w.Args, "--disable-bash") {
		t.Errorf("worker args %v", w.Args)
	}
	if got := readOrEmpty(filepath.Join(e.ws, "out", "answer.txt")); got != "ANSWER(int1)" {
		t.Errorf("outputFile=%q", got)
	}
}

func TestInputs(t *testing.T) {
	e := newEnv(t)
	cfg := `{"inputs":["Read README.md.","Then summarize it."]}`
	code, _, stderr := e.run("", cfg)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	want := []any{
		map[string]any{"type": "text", "text": "Read README.md."},
		map[string]any{"type": "text", "text": "Then summarize it."},
	}
	if got := interactionPosts(e.api.entries())[0]["input"]; !reflect.DeepEqual(got, want) {
		t.Errorf("input=%v", got)
	}
}

func TestGoalAndInputs(t *testing.T) {
	e := newEnv(t)
	code, _, stderr := e.run("the goal", `{"inputs":["Read README.md."]}`)
	if code != 2 || !strings.Contains(stderr, "set only one") {
		t.Fatalf("code=%d stderr=%s, want 2", code, stderr)
	}
	if n := len(e.api.entries()); n != 0 {
		t.Errorf("made %d API calls", n)
	}
}

func TestSession(t *testing.T) {
	e := newEnv(t)
	session := filepath.Join(e.runDir, "session-id")
	os.WriteFile(session, []byte("sess-1\n"), 0o644)
	for _, goal := range []string{"turn one", "turn two"} {
		if code, _, stderr := e.run(goal, "{}", "AX_SESSION_FILE="+session); code != 0 {
			t.Fatalf("%s: code=%d stderr=%s", goal, code, stderr)
		}
	}
	log := e.api.entries()
	nEnv := 0
	for _, l := range log {
		if l.Path == "/v1beta/environments" {
			nEnv++
		}
		if l.Method == "DELETE" {
			t.Errorf("session deleted its environment: %s", l.Path)
		}
	}
	if nEnv != 1 {
		t.Errorf("created %d environments, want 1", nEnv)
	}
	p := interactionPosts(log)
	if _, ok := p[0]["previous_interaction_id"]; ok || p[1]["previous_interaction_id"] != "int1" || p[1]["environment"] != "env1" {
		t.Errorf("turns %v", p)
	}
	path := filepath.Join(e.ws, ".ax", "sessions", "sess-1", stateFile)
	st, err := loadState(filepath.Dir(path))
	if err != nil || st.LastInteractionID != "int2" || st.Pending != nil || st.Environment.Key != "ek_env1" {
		t.Errorf("state %+v err %v", st, err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("state mode %v, want 0600", fi.Mode().Perm())
	}
}

func TestContinuation(t *testing.T) {
	e := newEnv(t)
	code, out, stderr := e.run("please continue", "{}")
	if code != 0 || out != "ANSWER(int2)\n" {
		t.Fatalf("code=%d stdout=%q stderr=%s", code, out, stderr)
	}
	p := interactionPosts(e.api.entries())
	if len(p) != 2 || p[1]["continuation_token"] != "tok1" || p[1]["previous_interaction_id"] != "int1" || p[1]["input"] != nil {
		t.Errorf("requests %v", p)
	}
}

func TestFailed(t *testing.T) {
	e := newEnv(t)
	code, _, stderr := e.run("status=failed", "{}")
	if code != 1 || !strings.Contains(stderr, "agent exploded") {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(e.ws, ".ax", "output")); !os.IsNotExist(err) {
		t.Error("failed interaction wrote an answer")
	}
	if l := e.api.entries(); l[len(l)-1].Method != "DELETE" {
		t.Error("failed one-off task kept its environment")
	}
}

func TestIncomplete(t *testing.T) {
	e := newEnv(t)
	if code, out, _ := e.run("status=incomplete", "{}"); code != 1 || out != "ANSWER(int1)\n" {
		t.Fatalf("code=%d stdout=%q, want 1 and the answer", code, out)
	}
}

func TestWorkerCrash(t *testing.T) {
	e := newEnv(t)
	t.Setenv("FAKE_WORKER_CRASH", "1")
	code, _, stderr := e.run("x", "{}")
	if code != 1 || !strings.Contains(stderr, "giving up") {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	if n := len(e.workers()); n != maxWorkerRestarts+1 {
		t.Errorf("worker started %d times, want %d", n, maxWorkerRestarts+1)
	}
}

func TestReattach(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	api, err := newClient(ctx, e.vars["GOOGLE_GEMINI_BASE_URL"], "test-key", "")
	if err != nil {
		t.Fatal(err)
	}
	envr, err := api.createEnvironment(ctx)
	if err != nil {
		t.Fatal(err)
	}
	id, err := api.createInteraction(ctx, map[string]any{"agent": defaultAgent, "environment": envr.ID, "input": "resume me"})
	if err != nil {
		t.Fatal(err)
	}
	cfgFile := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(cfgFile, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(cfgFile)
	if err != nil {
		t.Fatal(err)
	}
	st := &state{
		path:        filepath.Join(e.ws, ".ax", stateFile),
		Environment: envr,
		Pending:     &pending{ID: id, Digest: digestOf("resume me", cfg)},
	}
	if err := st.save(); err != nil {
		t.Fatal(err)
	}
	start := len(e.api.entries())
	code, out, stderr := e.run("resume me", "{}")
	if code != 0 || out != "ANSWER("+id+")\n" {
		t.Fatalf("code=%d stdout=%q stderr=%s", code, out, stderr)
	}
	if p := interactionPosts(e.api.entries()[start:]); len(p) != 0 {
		t.Errorf("relaunch created interactions %v", p)
	}
}

// TestSuspendResume stops the harness mid-interaction, as AX does when it
// suspends a task, and relaunches it with the same goal, as AX does on resume.
func TestSuspendResume(t *testing.T) {
	e := newEnv(t)
	os.WriteFile(e.vars["AX_GOAL_FILE"], []byte("hello"), 0o644)
	os.WriteFile(e.vars["AX_CONFIG_FILE"], []byte("{}"), 0o644)
	e.api.mu.Lock()
	e.api.hold = true
	e.api.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int)
	go func() {
		done <- run(ctx, func(k string) string { return e.vars[k] }, &bytes.Buffer{}, &bytes.Buffer{})
	}()
	for len(interactionPosts(e.api.entries())) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if code := <-done; code != 143 {
		t.Fatalf("code=%d, want 143", code)
	}
	for _, l := range e.api.entries() {
		if strings.HasSuffix(l.Path, "/cancel") || l.Method == "DELETE" {
			t.Fatalf("stop called %s %s; want the interaction and environment kept", l.Method, l.Path)
		}
	}

	e.api.mu.Lock()
	e.api.hold = false
	e.api.mu.Unlock()
	code, stdout, stderr := e.run("hello", "{}")
	if code != 0 || !strings.Contains(stdout, "ANSWER(int1)") || !strings.Contains(stderr, "reattaching to interaction int1") {
		t.Fatalf("code=%d stdout=%q stderr=%s", code, stdout, stderr)
	}
	if n := len(interactionPosts(e.api.entries())); n != 1 {
		t.Errorf("%d interactions created, want 1", n)
	}
	if l := e.api.entries(); l[len(l)-1].Method != "DELETE" {
		t.Errorf("environment not deleted after the resumed task finished")
	}
}

func TestBadInputs(t *testing.T) {
	e := newEnv(t)
	for _, cfg := range []string{
		`{"model":"x"}`,
		`{"base_agent":"a"}`,
		`{"input":"x"}`,
		`{"responseMimeType":"application/json"}`,
		`{"response_modalities":["TEXT"]}`,
		`{"system_instruction":"x"}`,
		`{"continuationToken":"t"}`,
		`{"inputs":"x"}`,
		`{"inputs":["ok",""]}`,
		`{"inputs":[1]}`,
		`{"agent":""}`,
		`{"environment":"env9"}`,
		`{"stream":true}`,
		`{"agentConfig":{"type":"nope"}}`,
		`{"agentConfig":{"type":"dynamic"}}`,
		`{"agentConfig":"antigravity"}`,
		`{"agentConfig":null}`,
		`{"commandTimeout":"5 minutes"}`,
		`{"agent":3}`,
		`{"disableBash":"yes"}`,
	} {
		if code, _, _ := e.run("x", cfg); code != 1 {
			t.Errorf("config %s: code=%d, want 1", cfg, code)
		}
	}
	if code, _, _ := e.run("", "{}"); code != 2 {
		t.Errorf("empty goal: code=%d, want 2", code)
	}
	if n := len(e.api.entries()); n != 0 {
		t.Errorf("bad inputs made %d API calls", n)
	}
}
