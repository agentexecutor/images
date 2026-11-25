# Antigravity Agent Image (`agents/antigravity`)

Pure mount-ready container image and launcher (`ax-harness`) for running
headless [Antigravity CLI (`agy`)](https://antigravity.google/docs/cli/install)
tasks under the AX in-container wrapper contract. Contains no AX runner binary
(`ax-task-runner` is mounted read-only by AX at `/opt/ax`).

## Requirement: AX Runner Volume

This image has no `ax-task-runner`, so it only starts as an AX task when
ax-server mounts a runner volume image: `ax install --runner-volume-image=...`
or `AX_RUNNER_VOLUME_IMAGE` on ax-server. Release manifests may leave it empty
(google/ax#625). Without it, AX runs `/usr/local/bin/ax-task-runner`, which
this image doesn't have, and the task fails to start.

## Example Task (published image)

Published images have no `agy` binary (see "Builds" below), so the task opts
in to the runtime install and its Gateway allows `storage.googleapis.com` and
the `agy` release manifest host alongside the Gemini API. With a bundled
build, drop `AGY_RUNTIME_INSTALL` and that rule. The Gateway adds the real API key outside
the sandbox; the task only sees a placeholder. The Secret setup is described under "Injecting credentials" in
google/ax's `docs/manifests.md`.

```yaml
apiVersion: ax.io/v1alpha1
kind: Gateway
metadata:
  name: antigravity-gw
  atespace: default
spec:
  egress:
    - tlsPassthrough:
        # Runtime agy install: the release manifest (latest version) and the
        # release bucket.
        hostnames:
          - "antigravity-cli-auto-updater-974169037036.us-central1.run.app"
          - "storage.googleapis.com"
        ports:
          numbers: [443]
    - https:
        hostnames: ["generativelanguage.googleapis.com"]
        effects:
          replaceHeaders:
            - header: x-goog-api-key
              credentialUri: "ate-secret://k8s.io/default/<namespace>/<secret>/api-key"
---
apiVersion: ax.io/v1alpha1
kind: Task
metadata:
  name: antigravity-example
  atespace: default
spec:
  profile:
    image: "ghcr.io/agentexecutor/antigravity@sha256:<digest>"
    resources:
      limits:
        cpu: "2"
        memory: "4Gi"
  gateway:
    name: antigravity-gw
  env:
    - name: AGY_RUNTIME_INSTALL
      value: "true"
    - name: GEMINI_API_KEY
      value: placeholder # replaced outside the sandbox by antigravity-gw
  goal:
    text: "Create /workspace/hello.py that prints hello, and run it."
```

The answer is reported as the task's `status.outcome.output`.

## Interactive Use (`ax ssh`)

An `ax ssh` session never runs the launcher, so it gets none of the setup
`ax-harness` does. A bundled build already has `agy` on `PATH`. With a
runtime-install build, install `agy` once per workspace; the image puts
`/workspace/.ax/bin` on `PATH` (after the system paths), so a copy installed
by an earlier goal task is found as well:

```bash
/usr/local/share/ax/install-agy.sh "" /workspace/.ax/bin  # runtime-install builds only
export SSL_CERT_FILE=/run/ate/egress-trust-bundle/trust-bundle.pem  # trust the egress gateway
agy --settings='{"modelProvider":"gemini"}'                         # with GEMINI_API_KEY set
```

## Launcher Contract (`/usr/local/bin/ax-harness`)

The mounted AX runner prepares `/workspace`, writes task inputs to files, and
invokes `/usr/local/bin/ax-harness` with `/workspace` as the working directory
(see `runner/launcher.go` in [google/ax](https://github.com/google/ax)):

- `AX_GOAL_FILE` *(required)* — Path to the file holding the task prompt (`spec.goal.text`), passed as-is via `--print=<goal>`.
- `AX_CONFIG_FILE` *(optional)* — Path to `spec.config` serialized as JSON. Every `camelCase` key maps to the matching `agy` `--kebab-case` CLI flag, with two special keys:
  - `outputFile` — Optional extra path where `agy`'s answer is also written.
  - `settings` — Merged into `--settings=<JSON>`.
- `AX_MCP_CONFIG_FILE` *(optional)* — Path to the Workspace's MCP servers (`{"mcpServers": {...}}`). Each `http` (`url`) or `stdio` (`command` + `args`) server is registered with `agy mcp add` before the task runs; a malformed file fails the task.
- `AX_OUTPUT_FILE` *(optional)* — Path where `agy`'s final answer is written (AX reports it as `status.outcome.output`). The answer is always printed to stdout/logs as well, and `ax-harness` exits with `agy`'s exit code.
- `AX_SESSION_FILE` — Not used yet: AX's `spec.session_id` does not map to an `agy` conversation ID (`--conversation`).
- **Defaults:** `--dangerously-skip-permissions` (headless runs cannot answer interactive prompts) and `--add-dir=<cwd>` (so relative file-tool paths resolve inside `/workspace`).
- **Authentication:**
  - When `GEMINI_API_KEY` is set (and `AGY_ADC_AUTH` is unset), `ax-harness` automatically sets `"modelProvider": "gemini"` in `--settings`; `GOOGLE_GEMINI_BASE_URL` optionally points requests at a gateway or mock endpoint.
  - When `AGY_ADC_AUTH=true`, `agy` authenticates via Application Default Credentials against `GOOGLE_CLOUD_PROJECT` (Vertex AI).
- **Egress TLS trust bundle:** When `/run/ate/egress-trust-bundle/trust-bundle.pem` is mounted by Agent Substrate, `ax-harness` defaults `SSL_CERT_FILE` and `CURL_CA_BUNDLE` to it so both runtime installation (`curl`) and `agy` trust the egress gateway's TLS certificate during HTTPS credential injection.

## `agy` Version

`install-agy.sh` installs the latest `agy` release by default. It reads the
release's version, URL, and SHA-512 from the Antigravity release manifest
(`antigravity-cli-auto-updater-974169037036.us-central1.run.app`, the one the
[official installer](https://antigravity.google/cli/install.sh) uses), accepts
only URLs in the Antigravity release bucket on `storage.googleapis.com`, and
verifies the tarball against the manifest's checksum. So image builds and
runtime installs at different times can get different `agy` versions.

If the manifest or the download fails, `install-agy.sh` falls back to the
version pinned in the script (`PINNED_VERSION`), whose release URL and SHA-512
are in the script itself. To install only that version, set `AGY_VERSION` (a
build arg, or a task env var for runtime installs) to it; other versions are
rejected. To bump the fallback, add the new release's execution ID and the
SHA-512 of each platform's tarball (computed from the downloaded files), then
update `PINNED_VERSION`.

The Dockerfile's `ARG AGY_VERSION` is `latest` or `PINNED_VERSION` (CI checks
this), and the image records it as `AGY_VERSION` and the
`io.agentexecutor.agy.version` label. With `latest`, `agy --version` gives the
version actually installed.

## Builds: Runtime Install vs. Bundled

- **Runtime install (`PREINSTALL_AGY=false`):** Ships Debian packages, `/usr/local/share/ax/install-agy.sh`, and `/usr/local/bin/ax-harness`, with no `agy` binary. A task must opt in with `AGY_RUNTIME_INSTALL=true`, which installs the latest `agy` into `/workspace/.ax/bin` (so a resumed sandbox reuses it) and needs egress to `storage.googleapis.com` and the release manifest host (without the manifest host, the pinned fallback is installed). Without the opt-in, a missing `agy` fails the task with exit code `127`.
- **Bundled (`PREINSTALL_AGY=true`, current default):** Installs the latest `agy` into `/usr/local/bin` during `docker build`, so tasks need no extra egress and `ax ssh` sessions have `agy` right away. An `agy` in the image always wins over a runtime install left in the workspace.

**Published images are always runtime-install for now:** `release.yaml` builds
`antigravity` with `PREINSTALL_AGY=false` until shipping the `agy` binary in a
public image is cleared (#15), whatever the Dockerfile's default is.

## Local Build & Test

```bash
./agents/antigravity/test_harness.sh

# Runtime-install image (no agy binary inside):
docker build --build-arg PREINSTALL_AGY=false -t ax-antigravity:runtime ./agents/antigravity
./agents/antigravity/verify_image.sh ax-antigravity:runtime runtime

# Bundled image (agy inside):
docker build --build-arg PREINSTALL_AGY=true -t ax-antigravity:bundled ./agents/antigravity
./agents/antigravity/verify_image.sh ax-antigravity:bundled bundled
```
