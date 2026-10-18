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
| `NAMESPACE` | `kubevirt-runners` | where the runner VMIs and their Secrets live |
| `VM_IMAGE` | required | the containerDisk, pinned by digest |
| `VM_CPU_CORES`, `VM_MEMORY` | `2`, `4Gi` | per runner VM |
| `RUNNER_IDLE_TIMEOUT` | `15m` | a VM that hasn't started a job this long after creation is removed |
| `LOG_LEVEL` | `info` | JSON logs |
| `METRICS_ADDR` | `:8080` | serves `/metrics` (Prometheus) and `/healthz` |

The scale set is looked up by name and created if missing, never deleted: restarts keep its
ID and the queued jobs. For every assigned job (up to `MAX_RUNNERS`) the scaler registers a
JIT runner and creates a cloud-init Secret plus a VirtualMachineInstance of the same name;
the VMI owns the Secret. The VMIs in the namespace are the state, so a restart neither loses
nor duplicates VMs.

Every 30 s a reaper deletes the VMs that are finished (`Succeeded`/`Failed`) or idle past
`RUNNER_IDLE_TIMEOUT` (stuck boot, failing image pull, or its job was taken by another
runner), and Secrets that never got their VMI as owner. It first removes the runner's
GitHub registration; GitHub refuses that while the runner runs a job, which keeps long jobs
safe. Warm runners (`MIN_RUNNERS`) are therefore replaced every `RUNNER_IDLE_TIMEOUT`.

### Metrics

The `gha_*` metrics copy ARC's listener metrics (names, labels, histogram buckets), so the
homelab "ARC runners" dashboard shows these runners too: `gha_assigned_jobs`,
`gha_running_jobs`, `gha_available_jobs`, `gha_registered_runners`, `gha_busy_runners`,
`gha_idle_runners`, `gha_desired_runners`, `gha_min_runners`, `gha_max_runners`,
`gha_started_jobs_total`, `gha_completed_jobs_total{job_result}`,
`gha_job_startup_duration_seconds`, `gha_job_execution_duration_seconds`. Unlike ARC, all of
them carry `name=<scale set>`, the job metrics included.

VM-specific:

| Metric | |
|---|---|
| `kubevirt_scaleset_vm_boot_duration_seconds` | VM created → its runner took a job; for a VM created for a waiting job, that is boot + runner start |
| `kubevirt_scaleset_vms{phase}` | runner VMs by VMI phase, as of the reaper's last pass |
| `kubevirt_scaleset_reaped_vms_total{reason}` | VMs deleted by the reaper: `finished`, `idle` |
| `kubevirt_scaleset_errors_total{operation}` | errors logged and retried: `list_vms`, `create_runner`, `reap` |

The Kubernetes client uses the pod's ServiceAccount, or `KUBECONFIG`.

```sh
GITHUB_APP_CLIENT_ID=… GITHUB_APP_INSTALLATION_ID=… \
GITHUB_APP_PRIVATE_KEY_FILE=~/.config/kubevirt-scaleset/github-app.pem go run ./cmd/scaler
```

Cluster side (KubeVirt, the node, the deployment): [homelab-cluster](https://github.com/milanoid-labs/homelab-cluster).
