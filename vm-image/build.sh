#!/usr/bin/env bash
# Builds the runner VM disk: the Ubuntu 24.04 cloud image plus the GitHub Actions runner,
# Docker CE and gh, written to out/disk.qcow2 (sparse, compressed).
#
# Runs on homelab-runners (Ubuntu 24.04 runner container, passwordless sudo) and needs
# guestfs-tools, linux-image-virtual and qemu-utils. There is no /dev/kvm in the pods, so
# libguestfs runs its helper VM in software emulation (TCG): slow, but no special access.
# The same script works on any Ubuntu machine with those packages (faster with KVM).
set -euo pipefail

cd "$(dirname "$0")"
# shellcheck source=versions.env
. ./versions.env

WORK=${WORK:-$PWD/work}
OUT=${OUT:-$PWD/out}
mkdir -p "$WORK" "$OUT"

# Download once, always verify against the pinned checksum.
fetch() {
  local url=$1 sha=$2 dest=$3
  if [ ! -f "$dest" ] || ! echo "$sha  $dest" | sha256sum -c --quiet 2>/dev/null; then
    curl -fsSL --retry 3 -o "$dest" "$url"
    echo "$sha  $dest" | sha256sum -c --quiet
  fi
}

fetch "https://cloud-images.ubuntu.com/noble/${UBUNTU_RELEASE}/noble-server-cloudimg-amd64.img" \
  "$UBUNTU_SHA256" "$WORK/base.img"
fetch "https://github.com/actions/runner/releases/download/v${RUNNER_VERSION}/actions-runner-linux-x64-${RUNNER_VERSION}.tar.gz" \
  "$RUNNER_SHA256" "$WORK/runner.tar.gz"
fetch "https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_linux_amd64.deb" \
  "$GH_SHA256" "$WORK/gh.deb"

# libguestfs reads the host kernel from /boot (root-only on Ubuntu), hence sudo.
export LIBGUESTFS_BACKEND=direct
export LIBGUESTFS_MEMSIZE=2048   # helper VM RAM; the runner container's limit is 4Gi
export TMPDIR=$WORK              # virt-sparsify's scratch overlay, on the pod's work volume
G="sudo -E"

# 1. Grow the image: the cloud image's root filesystem has <1 GB free, not enough for
#    Docker + runner. virt-resize copies it into a DISK_SIZE disk and expands sda1 (root).
rm -f "$WORK/build.qcow2"
qemu-img create -f qcow2 "$WORK/build.qcow2" "$DISK_SIZE"
$G virt-resize --expand /dev/sda1 "$WORK/base.img" "$WORK/build.qcow2"

# 2. Install everything inside the image (--network: the helper VM gets user-mode NAT).
$G virt-customize -a "$WORK/build.qcow2" --network --smp 2 \
  --install qemu-guest-agent,git,curl,jq,unzip,zip,dirmngr,gnupg,ca-certificates \
  --run-command 'install -m 0755 -d /etc/apt/keyrings
    curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
    echo "deb [arch=amd64 signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu noble stable" \
      > /etc/apt/sources.list.d/docker.list
    apt-get update
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin' \
  --copy-in "$WORK/gh.deb:/tmp" \
  --run-command 'dpkg -i /tmp/gh.deb && rm /tmp/gh.deb' \
  --run-command 'useradd --create-home --shell /bin/bash --groups docker,sudo runner' \
  --write '/etc/sudoers.d/runner:runner ALL=(ALL) NOPASSWD:ALL
' \
  --chmod '0440:/etc/sudoers.d/runner' \
  --mkdir /opt/actions-runner \
  --copy-in "$WORK/runner.tar.gz:/tmp" \
  --run-command 'tar -xzf /tmp/runner.tar.gz -C /opt/actions-runner && rm /tmp/runner.tar.gz
    /opt/actions-runner/bin/installdependencies.sh
    chown -R runner:runner /opt/actions-runner' \
  --mkdir /etc/actions-runner \
  --chmod '0700:/etc/actions-runner' \
  --copy-in files/actions-runner.service:/etc/systemd/system \
  --mkdir /etc/homelab-runner-vm \
  --run-command "{ echo ubuntu-cloud-image ${UBUNTU_RELEASE}; echo actions-runner ${RUNNER_VERSION}; echo gh ${GH_VERSION};
    dpkg-query -W -f '\${Package} \${Version}\n' docker-ce containerd.io docker-buildx-plugin git qemu-guest-agent;
    } > /etc/homelab-runner-vm/versions.txt" \
  --run-command 'apt-get clean && rm -rf /var/lib/apt/lists/*' \
  --run-command 'rm -f /etc/ssh/ssh_host_*' \
  --run-command 'cloud-init clean --logs --machine-id'

# What went in, for the image labels and the job summary.
$G virt-cat -a "$WORK/build.qcow2" /etc/homelab-runner-vm/versions.txt | tee "$OUT/versions.txt"

# 3. Drop unused blocks and compress: roughly 1-1.3 GB instead of several GB.
$G virt-sparsify --compress "$WORK/build.qcow2" "$OUT/disk.qcow2"
$G chown "$(id -u):$(id -g)" "$OUT/disk.qcow2"
qemu-img info "$OUT/disk.qcow2"
