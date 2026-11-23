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
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

const maxWorkerRestarts = 3

// worker runs `gemini-api environments connect`, which leases the
// environment's interactions and executes their tool calls in the workspace.
// It holds only the environment key: API credentials are removed from its
// environment.
type worker struct {
	h        *harness
	args     []string
	env      []string
	cmd      *exec.Cmd
	done     chan struct{}
	err      error
	restarts int
}

func newWorker(h *harness, env *environment, cfg config, wd, baseURL, sslCertFile string) *worker {
	bin := h.getenv("GEMINI_API_BIN")
	if bin == "" {
		bin = "gemini-api"
	}
	host, _ := os.Hostname()
	args := []string{
		bin, "environments", "connect",
		"--root-dir=" + wd,
		"--workdir=" + wd,
		"--max-concurrent=1",
		"--worker-id=ax-" + host,
	}
	if baseURL != "" {
		args = append(args, "--server-url="+baseURL)
	}
	if cfg.CommandTimeout != "" {
		args = append(args, "--command-timeout="+cfg.CommandTimeout)
	}
	if cfg.DisableBash {
		args = append(args, "--disable-bash")
	}
	var childEnv []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		switch name {
		case "GEMINI_API_KEY", "GOOGLE_API_KEY", "GEMINI_ACCESS_TOKEN", "GEMINI_ENVIRONMENT_KEY", "SSL_CERT_FILE":
			continue
		}
		childEnv = append(childEnv, kv)
	}
	childEnv = append(childEnv, "GEMINI_ENVIRONMENT_KEY="+env.Key, "GEMINI_NO_KEYRING=1")
	if sslCertFile != "" {
		childEnv = append(childEnv, "SSL_CERT_FILE="+sslCertFile)
	}
	return &worker{h: h, args: args, env: childEnv}
}

func (w *worker) start() error {
	cmd := exec.Command(w.args[0], w.args[1:]...)
	cmd.Env = w.env
	cmd.Stdout = w.h.stderr
	cmd.Stderr = w.h.stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting gemini-api environments connect: %w", err)
	}
	w.cmd, w.done = cmd, make(chan struct{})
	go func() {
		w.err = cmd.Wait()
		close(w.done)
	}()
	return nil
}

// check restarts the worker if it exited, up to maxWorkerRestarts times.
func (w *worker) check() error {
	select {
	case <-w.done:
	default:
		return nil
	}
	if w.restarts >= maxWorkerRestarts {
		return fmt.Errorf("gemini-api environments connect exited (%v); giving up", w.err)
	}
	w.restarts++
	w.h.logf("gemini-api environments connect exited (%v); restarting (%d/%d)", w.err, w.restarts, maxWorkerRestarts)
	return w.start()
}

// stop terminates the worker, killing it if it does not exit within 10s.
func (w *worker) stop() {
	if w.cmd == nil {
		return
	}
	select {
	case <-w.done:
		return
	default:
	}
	_ = w.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-w.done:
	case <-time.After(10 * time.Second):
		_ = w.cmd.Process.Kill()
		<-w.done
	}
}
