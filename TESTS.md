# End-to-end tests

These are integration tests for the images in this repository, written as
plain-language steps instead of scripts. A coding agent (such as Antigravity
or Claude Code) can run them: ask it to run the tests for an image,
and it follows the steps and reports which checks pass. A person can follow
them by hand too.

These tests run each image as a real AX task. An AX cluster is already set up
and reachable with the `ax` CLI (`ax get tasks`, `ax apply`, and so on). It
pulls images from the registry at `localhost:5001`, and it already has an AX
runner volume, so the images need no runner of their own.

A Gemini API key is in `GEMINI_API_KEY`.

## `agy-cli`

1. Build the image from `agy-cli/` for the host's architecture, tag it
   `localhost:5001/agy-cli:e2e`, and push it. Note the digest the push
   reports.
2. Create a Secret named `agy-cli-e2e-gemini` in the `ax-system` namespace
   with the key in `api-key`, and label it `ax.io/credential=true`.
3. Apply a Gateway named `agy-cli-e2e-gw` that allows
   `generativelanguage.googleapis.com` with an `https` rule and replaces the
   `x-goog-api-key` header with
   `ate-secret://k8s.io/default/ax-system/agy-cli-e2e-gemini/api-key`.
4. Apply a Task named `agy-cli-e2e` with:
   - the image pinned to the digest from step 1,
   - `4Gi` of memory and 2 CPUs,
   - the Gateway `agy-cli-e2e-gw`,
   - `GEMINI_API_KEY` set to `placeholder`,
   - `debug: true`, so `ax ssh` works,
   - the goal "Create /workspace/hello.py that prints hello, run it, and reply
     with its output."
5. The task starts out suspended. Run `ax resume task agy-cli-e2e`.
6. Check that `ax describe task agy-cli-e2e` shows `Running`.
7. Wait until `/workspace/.ax/output` in the task is not empty
   (`ax ssh agy-cli-e2e -- cat /workspace/.ax/output`).
8. Check that:
   - the output includes `hello`,
   - `python3 /workspace/hello.py` in the task prints `hello`,
   - `GEMINI_API_KEY` in the task is still `placeholder`, so the real key
     never entered the sandbox.
9. Delete the Task, the Gateway and the Secret.

## `agy`

1. Build the image from `agy/` for the host's architecture, tag it
   `localhost:5001/agy:e2e`, and push it. Note the digest the push reports.
2. Apply a Gateway named `agy-e2e-gw` that allows
   `generativelanguage.googleapis.com` on port 443 with a `tlsPassthrough`
   rule. The Gateway can't inject the key for this image yet, so the task
   gets the real key.
3. Apply a Task named `agy-e2e` with:
   - the image pinned to the digest from step 1,
   - the Gateway `agy-e2e-gw`,
   - `commands: ["/usr/local/bin/ax-antigravity"]`,
   - `GEMINI_API_KEY` set to the real key,
   - `debug: true`, so `ax ssh` works,
   - the goal "Create /workspace/hello.py that prints hello, run it, and reply
     with its output."
4. The task starts out suspended. Run `ax resume task agy-e2e`.
5. Check that `ax describe task agy-e2e` shows `Running`.
6. Wait until `/workspace/.ax/output` in the task is not empty
   (`ax ssh agy-e2e -- cat /workspace/.ax/output`).
7. Check that:
   - the output includes `hello`,
   - `python3 /workspace/hello.py` in the task prints `hello`, which shows the
     agent's tool calls ran in the sandbox,
   - no environment key is left in `/workspace/.ax` after the turn.
8. Delete the Task and the Gateway.
