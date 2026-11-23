# Antigravity Managed Agent Image (`antigravity`)

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
    image: "ghcr.io/agentexecutor/antigravity@sha256:<digest>"
  gateway:
    name: antigravity-gw
  commands: ["/usr/local/bin/ax-antigravity"]
  env:
    - name: GEMINI_API_KEY
      value: "<your-api-key>"
  config:
    agent: antigravity-preview-09-2026
    inputs:
      - "Create hello.py that prints hello."
      - "Then run it."
    previous_interaction_id: "<interaction-id>" # optional: continue an earlier interaction
    system_instruction: "Run the tests before you finish."
    agent_config:
      type: antigravity
      model: gemini-3.8-flash
    timeout: 30m
```

Instead of `commands` and `inputs`, a task can set `spec.goal.text`, which is
then the input. Setting both `spec.goal.text` and `inputs` is an error.

Add egress rules for anything the agent's commands need, such as `github.com`
or package registries.

## Config

Interaction fields in `spec.config` are sent as-is on the
[CreateInteraction](https://ai.google.dev/api/interactions) request. The
answer is printed and written to `<workspace>/.ax/output`.

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `agent` | string | `antigravity-preview-09-2026` | Managed agent to run. |
| `inputs` | list of strings | `spec.goal.text` | Text parts of the interaction's input. |
| `previous_interaction_id` | string | | Earlier interaction to continue from. |
| `system_instruction` | string | | System instruction for the agent. |
| `tools` | list | | Extra tools, such as `google_search` or remote MCP servers. |
| `agent_config` | object | | Agent settings, e.g. `{type: antigravity, model: gemini-3.8-flash}`. |
| `labels` | map of strings | | Labels for the interaction. |
| `response_format` | object | | JSON schema the answer must follow. |
| `safety_settings` | list | | Safety settings. |
| `service_tier` | string | | Service tier. |
| `webhook_config` | object | | Webhooks for interaction events. |
| `timeout` | duration | none | How long to wait for the interaction, measured from each launch. |
| `commandTimeout` | duration | `2m` | Limit for each `execute_bash` command. |
| `disableBash` | bool | `false` | Leave the agent only the file tools. |
| `outputFile` | string | | Extra path to write the answer to. |

Durations are Go durations such as `90s` or `15m`. Other keys are rejected,
including fields the API deprecated and `environment`, `background`, `stream`,
`store` and `continuation_token`, which the harness sets itself.

## Restarts

Each task gets a new environment, which is deleted when the task ends.

When AX suspends a task, the harness leaves the interaction running and exits.
On resume, AX relaunches it with the same config, and it reattaches to the
interaction instead of starting a new one. A relaunch after the interaction
finished does nothing. The state it needs is in `<workspace>/.ax`.

## Security

- `GEMINI_API_KEY` is removed from the tool worker's environment, so the agent's commands don't inherit it. They run as the same user as the harness, though, so treat the key as readable from the sandbox.
- The environment key is stored in `<workspace>/.ax` (mode `0600`) so a relaunched harness can reconnect.

## Build and Test

```bash
(cd antigravity && go test ./...)
docker build -t ax-antigravity ./antigravity
```
