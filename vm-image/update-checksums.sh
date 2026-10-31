#!/usr/bin/env bash
# Rewrites the SHA-256 pins next to the versions Renovate bumps: versions.env (Ubuntu cloud
# image, actions/runner, gh) and the vm-image workflow (crane). Renovate can bump a version
# but not its checksum, so renovate.json5 runs this as a postUpgradeTask with the bumped
# dependency's name. Without arguments it refreshes every pin.
#
#   vm-image/update-checksums.sh [ubuntu-cloud-image|actions/runner|cli/cli|google/go-containerregistry]...
#
# Needs curl and sha256sum. The GitHub assets are downloaded and hashed (~200 MB in all);
# the Ubuntu image (~600 MB) is taken from its SHA256SUMS, the same origin as the image.
set -euo pipefail

cd "$(dirname "$0")/.."
VERSIONS=vm-image/versions.env
WORKFLOW=.github/workflows/vm-image.yaml
# shellcheck source=versions.env
. "$VERSIONS"
CRANE_VERSION=$(sed -n 's/^  CRANE_VERSION: "\(.*\)"$/\1/p' "$WORKFLOW")

sha_of() { curl -fsSL --retry 3 "$1" | sha256sum | cut -d' ' -f1; }

# pin FILE PREFIX SHA: replaces what follows PREFIX on the line that starts with it.
pin() {
  local file=$1 prefix=$2 sha=$3 tmp
  [[ $sha =~ ^[0-9a-f]{64}$ ]] || { echo "$prefix: bad checksum '$sha'" >&2; exit 1; }
  grep -q "^$prefix" "$file" || { echo "$prefix: not found in $file" >&2; exit 1; }
  tmp=$(mktemp)
  sed "s|^\($prefix\).*|\1$sha|" "$file" > "$tmp"
  cat "$tmp" > "$file" && rm "$tmp"
  echo "$file: $prefix$sha"
}

# Plain assignments, not pin "$(…)": set -e ignores a failed substitution inside arguments,
# and the checksum of an empty download would look valid.
update() {
  local sha
  case $1 in
    ubuntu-cloud-image)
      sha=$(curl -fsSL --retry 3 "https://cloud-images.ubuntu.com/noble/${UBUNTU_RELEASE}/SHA256SUMS" |
        awk '{ sub(/^\*/, "", $2) } $2 == "noble-server-cloudimg-amd64.img" { print $1 }')
      pin "$VERSIONS" UBUNTU_SHA256= "$sha" ;;
    actions/runner)
      sha=$(sha_of "https://github.com/actions/runner/releases/download/v${RUNNER_VERSION}/actions-runner-linux-x64-${RUNNER_VERSION}.tar.gz")
      pin "$VERSIONS" RUNNER_SHA256= "$sha" ;;
    cli/cli)
      sha=$(sha_of "https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_linux_amd64.deb")
      pin "$VERSIONS" GH_SHA256= "$sha" ;;
    google/go-containerregistry)
      sha=$(sha_of "https://github.com/google/go-containerregistry/releases/download/v${CRANE_VERSION}/go-containerregistry_Linux_x86_64.tar.gz")
      pin "$WORKFLOW" '  CRANE_SHA256: ' "$sha" ;;
    *)
      echo "unknown dependency: $1" >&2; exit 1 ;;
  esac
}

[ $# -gt 0 ] || set -- ubuntu-cloud-image actions/runner cli/cli google/go-containerregistry
for dep; do update "$dep"; done
