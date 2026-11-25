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

# Installs the Antigravity CLI (agy) for the current Linux platform (glibc).
#
#   install-agy.sh [version] [dest-dir]
#
# version is "latest" (the default) or a version pinned below.
#
# latest: the release's version, URL, and SHA-512 are read from the
# Antigravity release manifest, the same one the official installer
# (https://antigravity.google/cli/install.sh) uses. The URL must point into
# the Antigravity release bucket. If the manifest or the download fails, the
# pinned fallback (PINNED_VERSION) is installed instead.
#
# pinned: the release URL and SHA-512 checksum are taken from this file, so
# the only network request is the tarball download and its checksum never
# comes from the network.
#
# To bump the fallback, add a case below with the release's execution ID and
# the SHA-512 of each platform's tarball, computed from the downloaded files,
# and update PINNED_VERSION.

set -euo pipefail

PINNED_VERSION="1.5.10"

version="${1:-latest}"
dest="${2:-${HOME:-/root}/.local/bin}"
GCS="${AGY_GCS_BASE_URL:-https://storage.googleapis.com/antigravity-public}"
MANIFESTS="${AGY_MANIFEST_BASE_URL:-https://antigravity-cli-auto-updater-974169037036.us-central1.run.app/manifests}"

case "$(uname -m)" in
  x86_64|amd64) platform="linux-x64" tarball="cli_linux_x64.tar.gz" manifest="linux_amd64.json" ;;
  aarch64|arm64) platform="linux-arm" tarball="cli_linux_arm64.tar.gz" manifest="linux_arm64.json" ;;
  *)
    echo "install-agy: unsupported architecture $(uname -m)" >&2
    exit 1
    ;;
esac

tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT

# install_release <version> <url> <sha512>: downloads, verifies, and installs
# one release. Returns non-zero without touching dest on any failure.
install_release() {
  local v="$1" url="$2" sha512="$3"
  rm -f "${tmp}/agy.tar.gz" "${tmp}/antigravity"
  curl -fsSL --connect-timeout 15 -o "${tmp}/agy.tar.gz" "${url}" || return 1
  echo "${sha512}  ${tmp}/agy.tar.gz" | sha512sum -c - >/dev/null || return 1
  tar -xzf "${tmp}/agy.tar.gz" -C "${tmp}" antigravity || return 1
  mkdir -p "${dest}" || return 1
  install -m 0755 "${tmp}/antigravity" "${dest}/agy" || return 1
  echo "install-agy: installed agy ${v} (${platform}) to ${dest}/agy" >&2
}

# install_pinned <version>: installs a version whose URL and checksum are
# pinned in this file.
install_pinned() {
  local v="$1" execution_id sha512
  case "${v}" in
    1.5.10)
      execution_id="5253791805734912"
      case "${platform}" in
        linux-x64) sha512="713ae840845d74736e2d4a96f5d84e3353d614e73a875793ddf500997ccee09595448cebfd8e94fdd90ce2a0fa97707bd1cc29763dcfe01c945d80c92234378f" ;;
        linux-arm) sha512="f9f5c7ebc47a8b8de536acca8116b1f5a43dca614cde94008fe5a510517b23535fb281d85416930d76e76478858bf3dc66ea6d1b0c9b6eeadcfabe4d8ef2a807" ;;
      esac
      ;;
    *)
      echo "install-agy: agy ${v} is not pinned in install-agy.sh (use \"latest\" or ${PINNED_VERSION})" >&2
      return 1
      ;;
  esac
  install_release "${v}" "${GCS}/antigravity-cli/${v}-${execution_id}/${platform}/${tarball}" "${sha512}"
}

# install_latest: installs the release named by the manifest.
install_latest() {
  local json v url sha512
  json="$(curl -fsSL --connect-timeout 15 --max-time 60 "${MANIFESTS}/${manifest}")" || {
    echo "install-agy: cannot fetch ${MANIFESTS}/${manifest}" >&2
    return 1
  }
  manifest_key() {
    printf '%s\n' "${json}" | sed -n 's/.*"'"$1"'"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1
  }
  v="$(manifest_key version)"
  url="$(manifest_key url)"
  sha512="$(manifest_key sha512)"
  if [[ ! "${v}" =~ ^[0-9]+(\.[0-9]+)+$ || ! "${sha512}" =~ ^[0-9a-f]{128}$ ]]; then
    echo "install-agy: malformed release manifest ${MANIFESTS}/${manifest}" >&2
    return 1
  fi
  # The manifest may only point into the release bucket.
  if [[ "${url}" != "${GCS}/antigravity-cli/"* || "${url}" == *".."* ]]; then
    echo "install-agy: manifest URL ${url} is outside ${GCS}/antigravity-cli/" >&2
    return 1
  fi
  install_release "${v}" "${url}" "${sha512}" || {
    echo "install-agy: cannot download or verify agy ${v} from ${url}" >&2
    return 1
  }
}

if [[ "${version}" == "latest" ]]; then
  if ! install_latest; then
    echo "install-agy: latest agy unavailable; falling back to pinned agy ${PINNED_VERSION}" >&2
    install_pinned "${PINNED_VERSION}"
  fi
else
  install_pinned "${version}"
fi
