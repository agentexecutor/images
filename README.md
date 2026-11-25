# AX Images

[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)

Prebuilt container images for [AX](https://github.com/agentexecutor), such as
agent images (Antigravity) and, later, Claude Code and MCP server images.

Images are built by this repository's CI and published to
`ghcr.io/agentexecutor/<image>:<tag>` for linux/amd64 and linux/arm64.

## Prebuilt task images

These are prebuilt AX task images: set one as a Task's `spec.profile.image`
to run that agent as an AX task. They are building blocks, and applications
can compose them, using them as they are or combining them with their own
tasks and images.

| Image | Description | Source | Reference |
| --- | --- | --- | --- |
| `agy-cli` | Headless [Antigravity CLI](https://antigravity.google/docs/cli/install) with the `ax-harness` launcher. Published without the `agy` binary; it is installed when the task starts. | [`agy-cli/`](agy-cli/README.md) | `ghcr.io/agentexecutor/agy-cli@sha256:<digest>` |
| `agy` | Runs a [Gemini API Managed Agent](https://ai.google.dev/gemini-api/docs/agents) (Antigravity by default); tool calls run in the task's sandbox. | [`agy/`](agy/README.md) | `ghcr.io/agentexecutor/agy@sha256:<digest>` |

`:latest` is never moved; reference images by digest. The digests of each
release are listed in the summary of its **Release Images** workflow run. See
[releasing](docs/releasing.md) for how releases are cut.

If you want something to be a prebuilt image, let us know by
[opening an issue](https://github.com/agentexecutor/images/issues).
