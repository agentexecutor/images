#!/usr/bin/env bash

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

# Verifies a built Antigravity image.
#
#   verify_image.sh <image> bundled|runtime
#
# bundled: the image contains agy in /usr/local/bin.
# runtime: the image contains no agy; ax-harness installs it at task start.
#
# The expected agy version is AGY_VERSION when set (a build with a non-default
# version), otherwise the Dockerfile's ARG AGY_VERSION. For "latest", a bundled
# image may contain any released agy version.

set -euo pipefail

img="${1:?usage: verify_image.sh <image> bundled|runtime}"
mode="${2:?usage: verify_image.sh <image> bundled|runtime}"
DIR="$(cd "$(dirname "$0")" && pwd)"

default="$(sed -n 's/^ARG AGY_VERSION=//p' "${DIR}/Dockerfile")"
fail() { echo "::error::${img} (${mode}): $*" >&2; exit 1; }
run() { docker run --rm --entrypoint sh "${img}" -c "$1"; }

[[ -n "${default}" ]] || fail "cannot read ARG AGY_VERSION from Dockerfile"
want="${AGY_VERSION:-${default}}"

# No AX runner in the image; AX mounts it at /opt/ax.
if run 'test -e /usr/local/bin/ax-task-runner || test -e /opt/ax/ax-task-runner'; then
  fail "image must not contain ax-task-runner"
fi
run 'test -x /usr/local/bin/ax-harness' || fail "missing /usr/local/bin/ax-harness"
run 'test -x /usr/local/share/ax/install-agy.sh' || fail "missing install-agy.sh"
run 'git --version && python3 --version' >/dev/null || fail "missing git or python3"

# The image records the agy version it installs.
label="$(docker image inspect --format '{{ index .Config.Labels "io.agentexecutor.agy.version" }}' "${img}")"
[[ "${label}" == "${want}" ]] || fail "label io.agentexecutor.agy.version=${label}, want ${want}"
env_version="$(run 'printf %s "${AGY_VERSION}"')"
[[ "${env_version}" == "${want}" ]] || fail "AGY_VERSION=${env_version}, want ${want}"

# A runtime install in the workspace never shadows the image's PATH.
path="$(run 'printf %s "${PATH}"')"
[[ "${path}" == *":/workspace/.ax/bin" ]] || fail "PATH must end with /workspace/.ax/bin: ${path}"

case "${mode}" in
  bundled)
    where="$(run 'command -v agy')" || fail "agy is not on PATH"
    [[ "${where}" == "/usr/local/bin/agy" ]] || fail "agy at ${where}, want /usr/local/bin/agy"
    got="$(docker run --rm --entrypoint agy "${img}" --version | tr -d '[:space:]')"
    if [[ "${want}" == "latest" ]]; then
      [[ "${got}" =~ ^[0-9]+(\.[0-9]+)+$ ]] || fail "agy --version=${got}, want a release version"
    else
      [[ "${got}" == "${want}" ]] || fail "agy --version=${got}, want ${want}"
    fi
    want="${want} (${got})"
    ;;
  runtime)
    if run 'command -v agy >/dev/null 2>&1'; then
      fail "runtime-install image must not contain agy"
    fi
    ;;
  *)
    fail "unknown mode ${mode}"
    ;;
esac

echo "verify_image: ${img} (${mode}) OK, agy ${want}"
