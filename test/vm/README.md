# Runner VM by hand

Boots one runner VM from the published image without the scaler, to test the image and its
boot contract (../../vm-image/README.md). The `VM smoke` workflow then runs a job on it.

Run from the repo root with an admin kubeconfig, right before booting (an unused JIT config
is a registered, offline runner). `IMAGE` is the reference from the vm-image run summary.

```sh
IMAGE=192.168.1.204:8081/docker-hosted/homelab-runner-vm:<tag>@sha256:<digest>
JIT_CONFIG=$(gh api -X POST orgs/milanoid-labs/actions/runners/generate-jitconfig \
  -f name=vm-test-1 -F runner_group_id=1 -f 'labels[]=homelab-vm-test' -f work_folder=_work \
  -q .encoded_jit_config)

kubectl create namespace vm-test
sed "s|\${JIT_CONFIG}|$JIT_CONFIG|" test/vm/user-data.tmpl |
  kubectl -n vm-test create secret generic vm-test-1-userdata --from-file=userdata=/dev/stdin
sed "s|IMAGE|$IMAGE|" test/vm/vmi.yaml | kubectl apply -f -
unset JIT_CONFIG
```

Watch it, then run `VM smoke` (Actions tab, `runs-on: homelab-vm-test`):

```sh
kubectl -n vm-test get vmi -w        # Scheduling -> Running -> Succeeded after the job
kubectl -n vm-test logs -f -l kubevirt.io=virt-launcher -c guest-console-log   # serial console
gh api orgs/milanoid-labs/actions/runners -q '.runners[] | "\(.name) \(.status) \(.busy)"'
```

Clean up with `kubectl delete namespace vm-test`. The runner deregisters itself after its
one job; if the VM never ran one, delete `vm-test-1` under org Settings -> Actions -> Runners.
