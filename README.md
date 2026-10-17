# kubevirt-scaleset

Ephemeral, VM-based GitHub Actions runners for the milanoid-labs homelab: every job runs in
its own throwaway KubeVirt VM on `kubevirt01` (a Proxmox VM joined to k3s, nested KVM).

| Path | What |
|---|---|
| [`vm-image/`](vm-image/) | The runner VM disk (Ubuntu 24.04 + runner + Docker), published as a KubeVirt containerDisk to the homelab Nexus |
| *(planned)* | The autoscaler: listens to GitHub via [actions/scaleset](https://github.com/actions/scaleset) and creates one VirtualMachineInstance per job |

Cluster side (KubeVirt, the node, the deployment): [homelab-cluster](https://github.com/milanoid-labs/homelab-cluster).
