# Antigravity CLI (`agy-cli`)

Runs the [Antigravity CLI (`agy`)](https://antigravity.google/docs/cli/install)
headless as an AX task. The agent and its tools run inside the task's sandbox,
in its workspace (`/workspace`). Like the other images, it needs an AX runner
volume.

## Example

```yaml
apiVersion: ax.io/v1alpha1
kind: Gateway
metadata:
  name: antigravity-gw
  atespace: default
spec:
  egress:
    - tlsPassthrough:
        hostnames: ["storage.googleapis.com"] # runtime agy install
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
    # Find the digest at https://github.com/orgs/agentexecutor/packages?repo_name=images
    image: "ghcr.io/agentexecutor/agy-cli@sha256:<digest>"
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

Published images don't include `agy`, so the task sets `AGY_RUNTIME_INSTALL`
to install it at startup, and the Gateway allows `storage.googleapis.com`. The
Gateway injects the real API key outside the sandbox; the task only sees a
placeholder. The Secret setup is described under "Injecting credentials" in
google/ax's `docs/manifests.md`.

Give the task at least `2Gi` of memory (`4Gi` recommended); the default
`512Mi` is too small for `agy` and its session state.

## Config

`spec.goal.text` is the prompt. Keys in `spec.config` are `agy` CLI flags in
camelCase (`addDir` for `--add-dir`), with two exceptions:

| Key | Type | Description |
| --- | --- | --- |
| `settings` | object | `agy` settings, merged into `--settings`. |
| `outputFile` | string | Extra path to write the answer to. |

The answer is printed and written to `<workspace>/.ax/output`.

| Environment variable | Description |
| --- | --- |
| `GEMINI_API_KEY` | Gemini API key. |
| `AGY_ADC_AUTH` | `true` to use Application Default Credentials and Vertex AI (`GOOGLE_CLOUD_PROJECT`) instead. |
| `GOOGLE_GEMINI_BASE_URL` | Alternative Gemini API endpoint. |
| `AGY_RUNTIME_INSTALL` | `true` to install `agy` at startup. Needs egress to `storage.googleapis.com`. |

## MCP servers

The Workspace's MCP servers are added to `agy`. They run in the sandbox, so
their endpoints must be reachable from the task.

## Restarts

When `spec.sessionId` is set, the conversation is kept in the workspace, so it
survives suspend and resume, and follow-up turns in the same session continue
it.

## Security

- With an `https` Gateway rule, the API key never enters the sandbox.
- Only pinned `agy` versions are installed, and each download is verified.
