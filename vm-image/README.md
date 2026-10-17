# Runner VM image

An Ubuntu 24.04 disk with the GitHub Actions runner, Docker CE and `gh`, published as a
KubeVirt **containerDisk**:

```
192.168.1.204:8081/docker-hosted/homelab-runner-vm:<YYYYMMDD>-<commit>-<run number>.<attempt>
```

Every runner VM boots this image read-only (KubeVirt adds a throwaway copy-on-write
overlay), runs exactly one job and powers off. Nothing survives between jobs.

## Contents

| | Version | Pinned in |
|---|---|---|
| Ubuntu cloud image | dated build `UBUNTU_RELEASE` | `versions.env` (+ SHA-256) |
| actions/runner | `RUNNER_VERSION` | `versions.env` (+ SHA-256) |
| gh | `GH_VERSION` | `versions.env` (+ SHA-256) |
| Docker CE + CLI, buildx, compose, containerd | latest from download.docker.com at build time | recorded in `/etc/homelab-runner-vm/versions.txt` and the image label `io.homelab.runner-vm.versions` |
| git, curl, jq, unzip, zip, dirmngr, qemu-guest-agent | Ubuntu archive at build time | same |

Keep `RUNNER_VERSION` and `GH_VERSION` equal to homelab-cluster's `runner/Dockerfile`, so a
workflow behaves the same on `homelab-runners` (pods) and `homelab-vm` (VMs).

User `runner` (uid from `useradd`) is in `docker` and `sudo`, with passwordless sudo, like
the pod runners and GitHub-hosted runners. The runner lives in `/opt/actions-runner`.

## Boot contract

The image does nothing on its own: `actions-runner.service` is installed but not enabled.
The VM's cloud-init user-data (from the scaler, as a Secret via
`cloudInitNoCloud.secretRef`, never inline: the JIT config is a credential) must:

```yaml
#cloud-config
write_files:
  - path: /etc/actions-runner/jitconfig
    owner: root:root
    permissions: "0600"
    content: <encoded_jit_config from GenerateJitRunnerConfig>
runcmd:
  - [systemctl, start, --no-block, actions-runner.service]
```

The unit then runs `run.sh --jitconfig …` as `runner` (the file reaches it via systemd
`LoadCredential`), and **powers the VM off when the runner exits**, however it ends. The
VMI turns `Succeeded` and the scaler deletes it with its Secret. A VM booted without this
user-data never starts the runner, and the scaler's boot timeout reaps it.

## Build

CI (`.github/workflows/vm-image.yaml`) runs on `homelab-runners`: PRs build only, `main`
builds and pushes with the Nexus user `ci-push` (repo secret `NEXUS_PUSH_PASSWORD`,
variables `NEXUS_PUSH_USERNAME`, `NEXUS_REGISTRY`). The pods have no `/dev/kvm`, so
libguestfs emulates and a build takes a while.

`build.sh` first probes the network of libguestfs' helper VM (~3 min). If it has no IP or
DNS, the build stops there and the `build disk` log shows what the helper VM saw (DHCP,
`resolv.conf`), instead of apt failing after the ~15 min resize.

Locally, on Ubuntu 24.04:

```sh
sudo apt-get install guestfs-tools linux-image-virtual qemu-utils
vm-image/build.sh          # -> vm-image/out/disk.qcow2, out/versions.txt
```

Take the disk out of a published image:

```sh
podman pull --tls-verify=false 192.168.1.204:8081/docker-hosted/homelab-runner-vm:<tag>
podman create --name tmp 192.168.1.204:8081/docker-hosted/homelab-runner-vm:<tag> x
podman cp tmp:/disk/disk.qcow2 . && podman rm tmp
```

## Testing without the scaler

A JIT config can also be made with the REST API, so a single VM can be tested by hand:

```sh
gh api -X POST orgs/milanoid-labs/actions/runners/generate-jitconfig \
  -f name=vm-test-1 -F runner_group_id=1 -f 'labels[]=homelab-vm-test' -f work_folder=_work \
  -q .encoded_jit_config
```

Put it into the user-data above, boot a VMI from the image on kubevirt01, and run a
`workflow_dispatch` job with `runs-on: homelab-vm-test`.
