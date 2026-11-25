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
#   verify_image.sh <image>

set -euo pipefail

img="${1:?usage: verify_image.sh <image>}"

fail() { echo "::error::${img}: $*" >&2; exit 1; }
run() { docker run --rm --entrypoint sh "${img}" -c "$1"; }

# No AX runner in the image; AX mounts it at /opt/ax.
if run 'test -e /usr/local/bin/ax-runner || test -e /opt/ax/ax-runner || test -e /usr/local/bin/ax-task-runner || test -e /opt/ax/ax-task-runner'; then
  fail "image must not contain ax-runner or ax-task-runner"
fi
run 'test -x /usr/local/bin/ax-launcher' || fail "missing /usr/local/bin/ax-launcher"
run 'test -x /usr/local/bin/ax-harness' || fail "missing /usr/local/bin/ax-harness"
run 'git --version && python3 --version' >/dev/null || fail "missing git or python3"

where="$(run 'command -v agy')" || fail "agy is not on PATH"
[[ "${where}" == "/usr/local/bin/agy" ]] || fail "agy at ${where}, want /usr/local/bin/agy"
version="$(docker run --rm --entrypoint agy "${img}" --version | tr -d '[:space:]')" || fail "agy --version failed"

echo "verify_image: ${img} OK, agy ${version}"
