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

# Pure Antigravity (agy) agent image for AX: no AX runner baked in.
#
# The image installs the latest Antigravity CLI into /usr/local/bin at build
# time, alongside Debian packages and /usr/local/bin/ax-launcher.
FROM debian:bookworm-slim@sha256:734a72b3d6142a71993b4bc580ca01d8ca687f77a2c422d96a961d3376b179bc

RUN apt-get update && apt-get install -y --no-install-recommends \
        bash \
        ca-certificates \
        curl \
        git \
        jq \
        openssh-client \
        procps \
        python3 \
    && rm -rf /var/lib/apt/lists/*

# Installs the latest agy with the official installer, which verifies the
# download's SHA-512 against the release manifest. Auto-update is disabled so
# a task keeps the version it was built with.
RUN curl -fsSL https://antigravity.google/cli/install.sh | bash -s -- --dir /usr/local/bin \
    && rm -rf /root/.cache/antigravity \
    && agy --version

ENV AGY_CLI_DISABLE_AUTO_UPDATE=true

COPY ax-harness /usr/local/bin/ax-launcher
RUN chmod 0755 /usr/local/bin/ax-launcher \
    && ln -s ax-launcher /usr/local/bin/ax-harness
