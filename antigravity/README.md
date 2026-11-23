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

AX only starts an image's launcher (`/usr/local/bin/ax-harness`, a link to
`ax-antigravity`) for tasks with `spec.goal`, so a task without one runs
`ax-antigravity` as its command. A task can set `spec.goal.text`
instead of `commands` and `inputs`: the goal is then the input.

Add egress rules for anything the agent's commands need, such as `github.com`
or package registries.

## Config

`spec.config` is an interaction request: `agent`, `previous_interaction_id`,
`system_instruction`, `tools`, `agent_config` and the other
[CreateInteraction](https://ai.google.dev/api/interactions) fields are sent
as-is. `agent` defaults to `antigravity-preview-09-2026`. `inputs` is a list of
strings, sent as the text parts of the interaction's input; without it, the
input is `spec.goal.text`. The answer is printed and written to
`AX_OUTPUT_FILE` (`<workspace>/.ax/output`). The harness sets `environment`, `background`, `stream`,
`store` and `continuation_token` itself.

Harness options:

- `timeout`: how long to wait for the interaction (Go duration), measured from each launch.
- `commandTimeout`: limit for each `execute_bash` command.
- `disableBash`: `true` leaves the agent only the file tools.
- `outputFile`: an extra path to write the answer to.

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
