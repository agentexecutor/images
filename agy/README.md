# Antigravity Managed Agent Image (`agy`)

Runs a [Gemini API Managed Agent](https://ai.google.dev/gemini-api/docs/agents)
(Antigravity by default) as an AX task. The agent loop runs in the Gemini API;
every tool it calls (`execute_bash`, file edits, search) runs inside the task's
sandbox, in its workspace (`/workspace` unless the Workspace binding sets a
path).

For each task, `ax-antigravity` creates a
[self-hosted environment](https://github.com/google-gemini/gemini-api-cli/tree/main/docs/self-hosted),
runs `gemini-api environments connect` in the workspace to execute the agent's
tool calls, and creates an interaction from `spec.config`. Like the other
images, it needs an AX runner volume.

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
        hostnames: ["generativelanguage.googleapis.com"]
        ports:
          numbers: [443]
---
apiVersion: ax.io/v1alpha1
kind: Task
metadata:
  name: antigravity-example
  atespace: default
spec:
  profile:
    image: "ghcr.io/agentexecutor/agy@sha256:<digest>"
  gateway:
    name: antigravity-gw
  commands: ["/usr/local/bin/ax-antigravity"]
  env:
    - name: GEMINI_API_KEY
      value: "<your-api-key>"
  config:
    agent: antigravity-preview-09-2026
    agentConfig:
      model: gemini-3.8-flash
    inputs:
      - "Create hello.py that prints hello."
      - "Then run it."
    previousInteractionId: "<interaction-id>" # optional: continue an earlier interaction
    systemInstruction: "Run the tests before you finish."
    timeout: 30m
```

Instead of `commands` and `inputs`, a task can set `spec.goal.text`, which is
then the input. Setting both `spec.goal.text` and `inputs` is an error.

Add egress rules for anything the agent's commands need, such as `github.com`
or package registries.

## Config

Interaction fields in `spec.config` are the camelCase names of the
[CreateInteraction](https://ai.google.dev/api/interactions) request fields
(`systemInstruction` for `system_instruction`); their values are sent as-is. The
answer is printed and written to `<workspace>/.ax/output`.

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `agent` | string | `antigravity-preview-09-2026` | Managed agent to run. |
| `agentConfig` | object | `{type: antigravity}` | Agent settings, e.g. `{model: gemini-3.8-flash}`. `type` is always `antigravity`. |
| `inputs` | list of strings | `spec.goal.text` | Text parts of the interaction's input. |
| `previousInteractionId` | string | | Earlier interaction to continue from. |
| `systemInstruction` | string | | System instruction for the agent. |
| `tools` | list | | Extra tools, such as `google_search` or remote MCP servers. The Workspace's MCP servers are added to them. |
| `labels` | map of strings | | Labels for the interaction. |
| `responseFormat` | object | | JSON schema the answer must follow. |
| `safetySettings` | list | | Safety settings. |
| `serviceTier` | string | | Service tier. |
| `webhookConfig` | object | | Webhooks for interaction events. |
| `timeout` | duration | none | How long to wait for the interaction, measured from each launch. |
| `commandTimeout` | duration | `2m` | Limit for each `execute_bash` command. |
| `disableBash` | bool | `false` | Leave the agent only the file tools. |
| `outputFile` | string | | Extra path to write the answer to. |

Durations are Go durations such as `90s` or `15m`. Other keys are rejected,
including fields the API deprecated and `environment`, `background`, `stream`,
`store` and `continuationToken`, which the harness sets itself.

## MCP servers

The Workspace's MCP servers are added to the interaction's `tools`
automatically. The Gemini API calls them, not the sandbox, so:

- only servers with an `endpoint` are added; servers with a `command` are skipped;
- the endpoint must be reachable from the Gemini API, not just from the task;
- a server with the same name in `spec.config.tools` keeps its definition there, for example to add `headers` or `allowed_tools`.

## Restarts

When `AX_SESSION_FILE` is empty, each task gets a new environment that is
deleted when the task ends. When `AX_SESSION_FILE` carries a session ID, the
environment is kept under `<workspace>/.ax/sessions/<id>` so follow-up turns
reuse it.

Note: `ax-server` in `google/ax` currently defaults `spec.sessionId` to
`metadata.uid` when omitted, so every server-created task passes a non-empty
`AX_SESSION_FILE` and retains its remote environment until that contract is
updated.

When AX suspends a task, the harness leaves the interaction running and exits.
On resume, AX relaunches it with the same config, and it reattaches to the
interaction instead of starting a new one. A relaunch after the interaction
finished does nothing. The state it needs is in `<workspace>/.ax`.

## Security

- `GEMINI_API_KEY` is removed from the tool worker's environment, so the agent's commands don't inherit it. They run as the same user as the harness, though, so treat the key as readable from the sandbox.
- The environment key is stored in `<workspace>/.ax` (mode `0600`) so a relaunched harness can reconnect.
- The Gateway can't inject `GEMINI_API_KEY` yet. Injecting it needs an `https` rule with `replaceHeaders`, but the tool worker connects to the same host over a WebSocket, and the egress gateway rejects WebSocket upgrades on `https` rules. Use `tlsPassthrough` and pass the key in `spec.env` until it supports them.

## Build and Test

```bash
(cd agy && go test ./...)
docker build -t ax-agy ./agy
```
