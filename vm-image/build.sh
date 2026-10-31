#!/usr/bin/env bash
# Builds the runner VM disk: the Ubuntu 24.04 cloud image plus the GitHub Actions runner,
# Docker CE and gh, written to out/disk.qcow2 (sparse, compressed).
# Needs guestfs-tools, linux-image-virtual, qemu-utils and isc-dhcp-client (see the workflow).
# Without /dev/kvm (the homelab-runners pods) libguestfs emulates: slow, but it works.
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

export LIBGUESTFS_BACKEND=direct
export LIBGUESTFS_MEMSIZE=2048   # helper VM RAM; the runner container's limit is 4Gi
G="sudo -E"                      # libguestfs reads the kernel in /boot, root-only on Ubuntu

# 1. The cloud image's root filesystem has <1 GB free: grow the disk to DISK_SIZE, then
#    (first step below) partition 1 and its filesystem in place, like cloud-init does on
#    first boot. Not virt-resize: it renumbers the partitions (root sits after 14/15/16 on
#    disk), and BIOS GRUB then stops at "no such partition".
rm -f "$WORK/build.qcow2"
qemu-img convert -O qcow2 "$WORK/base.img" "$WORK/build.qcow2"
qemu-img resize "$WORK/build.qcow2" "$DISK_SIZE"

# 2. Install everything. --update: Ubuntu's updates since the cloud image was built (apt-get
#    upgrade, so a new kernel ABI is held back until Renovate bumps UBUNTU_RELEASE); after the
#    growpart, it needs the space. dirmngr: sonarqube-scan-action verifies its scanner with GPG.
#    .env: environment for every job; ImageOS is what setup-* actions use to pick builds.
$G virt-customize -a "$WORK/build.qcow2" --network --smp 2 \
  --run-command 'growpart /dev/sda 1 && resize2fs /dev/sda1' \
  --update \
  --install qemu-guest-agent,git,jq,unzip,zip,dirmngr,gnupg \
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
  --write '/opt/actions-runner/.env:ImageOS=ubuntu24
' \
  --copy-in "$WORK/runner.tar.gz:/tmp" \
  --run-command 'tar -xzf /tmp/runner.tar.gz -C /opt/actions-runner && rm /tmp/runner.tar.gz
    /opt/actions-runner/bin/installdependencies.sh
    chown -R runner:runner /opt/actions-runner' \
  --mkdir /etc/actions-runner \
  --chmod '0700:/etc/actions-runner' \
  --copy-in files/actions-runner.service:/etc/systemd/system \
  --mkdir /etc/homelab-runner-vm \
  --run-command "{ echo ubuntu-cloud-image ${UBUNTU_RELEASE}; echo actions-runner ${RUNNER_VERSION}; echo gh ${GH_VERSION};
    dpkg-query -W -f '\${Package} \${Version}\n' docker-ce docker-ce-cli containerd.io docker-buildx-plugin \
      docker-compose-plugin git jq unzip zip dirmngr gnupg qemu-guest-agent;
    } > /etc/homelab-runner-vm/versions.txt" \
  --run-command 'apt-get clean && rm -rf /var/lib/apt/lists/*' \
  --run-command 'cloud-init clean --logs' \
  --truncate /etc/machine-id \
  --run-command 'test "$(findmnt -no SOURCE /)" = /dev/sda1 && test -f /etc/machine-id && test ! -s /etc/machine-id'
# cloud-init clean --machine-id would delete the file here (no systemd running in the build),
# and without it networkd's DHCP client fails. Empty is systemd's "first boot, make an ID".
# The last command fails the build unless root is still partition 1 and that file is empty.

$G virt-cat -a "$WORK/build.qcow2" /etc/homelab-runner-vm/versions.txt | tee "$OUT/versions.txt"

# 3. Trim unused blocks, then compress (~1 GB). virt-sparsify's copy mode needs 20G scratch
#    space and prompts interactively when short, so trim in place and convert with qemu-img.
$G virt-sparsify --in-place "$WORK/build.qcow2"
$G qemu-img convert -c -O qcow2 "$WORK/build.qcow2" "$OUT/disk.qcow2"
$G chown "$(id -u):$(id -g)" "$OUT/disk.qcow2"
qemu-img info "$OUT/disk.qcow2"
