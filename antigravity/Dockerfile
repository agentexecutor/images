# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Gemini API Managed Agents (Antigravity) image for AX: no AX runner baked in.
#
# The agent loop runs in the Gemini API. The image has the gemini-api CLI,
# whose `environments connect` runs the agent's tool calls in the task's
# workspace, and /usr/local/bin/ax-antigravity, which creates a self-hosted
# environment and the interaction for each task. /usr/local/bin/ax-harness,
# the launcher AX starts for tasks with a goal, links to it.
FROM golang:1.26-bookworm@sha256:dc40a13b61304647ed7e5d581d246b86413dfc29d63c6d018310feb93cd98fc0 AS build

# The gemini-api release this image installs, recorded in the image as
# GEMINI_API_CLI_VERSION and a label.
ARG GEMINI_API_CLI_VERSION=v0.5.0

RUN CGO_ENABLED=0 GOTOOLCHAIN=auto go install -trimpath -ldflags="-s -w" \
        "github.com/google-gemini/gemini-api-cli/cmd/gemini-api@${GEMINI_API_CLI_VERSION}"

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /go/bin/ax-antigravity .

FROM debian:bookworm-slim@sha256:734a72b3d6142a71993b4bc580ca01d8ca687f77a2c422d96a961d3376b179bc

# Tools the agent's execute_bash commands commonly reach for.
RUN apt-get update && apt-get install -y --no-install-recommends \
        bash \
        ca-certificates \
        curl \
        git \
        jq \
        openssh-client \
        procps \
        python3 \
        ripgrep \
    && rm -rf /var/lib/apt/lists/*

ARG GEMINI_API_CLI_VERSION=v0.5.0
LABEL io.agentexecutor.gemini-api.version="${GEMINI_API_CLI_VERSION}"
ENV GEMINI_API_CLI_VERSION="${GEMINI_API_CLI_VERSION}"

COPY --from=build /go/bin/gemini-api /go/bin/ax-antigravity /usr/local/bin/
RUN ln -s ax-antigravity /usr/local/bin/ax-harness && gemini-api version
