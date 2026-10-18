# kubevirt-scaleset

Ephemeral, VM-based GitHub Actions runners for the milanoid-labs homelab: every job runs in
its own throwaway KubeVirt VM on `kubevirt01` (a Proxmox VM joined to k3s, nested KVM).

| Path | What |
|---|---|
| [`vm-image/`](vm-image/) | The runner VM disk (Ubuntu 24.04 + runner + Docker), published as a KubeVirt containerDisk to the homelab Nexus |
| [`cmd/scaler/`](cmd/scaler/), [`internal/`](internal/) | The autoscaler (in progress): listens to GitHub via [actions/scaleset](https://github.com/actions/scaleset) and creates one VirtualMachineInstance per job |
| [`test/vm/`](test/vm/) | Booting one runner VM by hand, without the scaler |

## Scaler

Configured by environment variables (defaults in [`internal/config`](internal/config/config.go)):

| Variable | Default | |
|---|---|---|
| `GITHUB_CONFIG_URL` | `https://github.com/milanoid-labs` | where the scale set is registered |
| `SCALE_SET_NAME` | `homelab-runners-vm` | also the label: `runs-on: homelab-runners-vm` |
| `RUNNER_GROUP` | `default` | |
| `GITHUB_APP_CLIENT_ID` | required | GitHub App `milanoid-labs-kubevirt-runners` (Client ID or App ID) |
| `GITHUB_APP_INSTALLATION_ID` | required | |
| `GITHUB_APP_PRIVATE_KEY_FILE` | required | path to the PEM (a mounted Secret), never the key itself |
| `MIN_RUNNERS`, `MAX_RUNNERS` | `0`, `2` | |
| `LOG_LEVEL` | `info` | JSON logs |

The scale set is looked up by name and created if missing, never deleted: restarts keep its
ID and the queued jobs.

```sh
GITHUB_APP_CLIENT_ID=… GITHUB_APP_INSTALLATION_ID=… \
GITHUB_APP_PRIVATE_KEY_FILE=~/.config/kubevirt-scaleset/github-app.pem go run ./cmd/scaler
```

Cluster side (KubeVirt, the node, the deployment): [homelab-cluster](https://github.com/milanoid-labs/homelab-cluster).
