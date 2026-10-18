package scaler

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/actions/scaleset"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/milanoid-labs/kubevirt-scaleset/internal/vm"
)

// agedVMI is a scale set VMI created age ago, registered as runner id.
func agedVMI(name, id string, phase kubevirtv1.VirtualMachineInstancePhase, age time.Duration) *kubevirtv1.VirtualMachineInstance {
	v := existingVMI(name, phase)
	v.CreationTimestamp = metav1.NewTime(time.Now().Add(-age))
	v.Annotations = map[string]string{vm.RunnerIDAnnotation: id}
	return v
}

func exists(t *testing.T, s *Scaler, obj client.Object, name string) bool {
	t.Helper()
	err := s.Kube.Get(context.Background(), client.ObjectKey{Namespace: "runners", Name: name}, obj)
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
	return err == nil
}

func TestReapDeletesFinishedVMs(t *testing.T) {
	s, gh := newScaler(t,
		agedVMI("done", "1", kubevirtv1.Succeeded, time.Minute),
		agedVMI("crashed", "2", kubevirtv1.Failed, time.Minute),
		agedVMI("working", "3", kubevirtv1.Running, time.Minute),
	)
	s.Reap(context.Background())

	for name, want := range map[string]bool{"done": false, "crashed": false, "working": true} {
		if got := exists(t, s, &kubevirtv1.VirtualMachineInstance{}, name); got != want {
			t.Errorf("VMI %s exists = %v, want %v", name, got, want)
		}
	}
	if len(gh.removed) != 2 {
		t.Errorf("removed runners %v, want 1 and 2", gh.removed)
	}
}

func TestReapIdleVMButNotBusyOne(t *testing.T) {
	s, gh := newScaler(t,
		agedVMI("idle", "1", kubevirtv1.Running, 20*time.Minute),
		agedVMI("long-job", "2", kubevirtv1.Running, 3*time.Hour),
		agedVMI("booting", "3", kubevirtv1.Scheduling, 2*time.Minute),
	)
	gh.busy = map[int64]error{2: scaleset.JobStillRunningError}
	s.Reap(context.Background())

	for name, want := range map[string]bool{"idle": false, "long-job": true, "booting": true} {
		if got := exists(t, s, &kubevirtv1.VirtualMachineInstance{}, name); got != want {
			t.Errorf("VMI %s exists = %v, want %v", name, got, want)
		}
	}
}

func TestReapKeepsVMWhenRunnerRemovalFails(t *testing.T) {
	// Unknown errors (GitHub down) keep the VM: a later pass retries, so no runner is
	// left registered without its VM.
	s, gh := newScaler(t, agedVMI("done", "1", kubevirtv1.Succeeded, time.Minute))
	gh.busy = map[int64]error{1: context.DeadlineExceeded}
	s.Reap(context.Background())
	if !exists(t, s, &kubevirtv1.VirtualMachineInstance{}, "done") {
		t.Error("VMI deleted although its runner is still registered")
	}
}

func TestReapAlreadyRemovedRunner(t *testing.T) {
	s, gh := newScaler(t, agedVMI("done", "1", kubevirtv1.Succeeded, time.Minute))
	gh.busy = map[int64]error{1: scaleset.RunnerNotFoundError}
	s.Reap(context.Background())
	if exists(t, s, &kubevirtv1.VirtualMachineInstance{}, "done") {
		t.Error("VMI kept although its runner is gone")
	}
}

// agedSecret is a scale set Secret created age ago for runner id, owned by a VMI or not.
func agedSecret(name, id string, age time.Duration, owned bool) *corev1.Secret {
	sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name: name, Namespace: "runners", CreationTimestamp: metav1.NewTime(time.Now().Add(-age)),
		Labels:      map[string]string{vm.ManagedByLabel: vm.ManagedBy, vm.ScaleSetLabel: "homelab-runners-vm"},
		Annotations: map[string]string{vm.RunnerIDAnnotation: id},
	}}
	if owned {
		sec.OwnerReferences = []metav1.OwnerReference{{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachineInstance", Name: "x", UID: "u"}}
	}
	return sec
}

func TestReapOrphanSecrets(t *testing.T) {
	s, gh := newScaler(t,
		agedSecret("orphan-userdata", "1", 10*time.Minute, false),
		agedSecret("just-created-userdata", "2", 10*time.Second, false),
		agedSecret("owned-userdata", "3", 10*time.Minute, true),
	)
	s.Reap(context.Background())

	for name, want := range map[string]bool{"orphan-userdata": false, "just-created-userdata": true, "owned-userdata": true} {
		if got := exists(t, s, &corev1.Secret{}, name); got != want {
			t.Errorf("secret %s exists = %v, want %v", name, got, want)
		}
	}
	if len(gh.removed) != 1 || gh.removed[0] != 1 {
		t.Errorf("removed runners %v, want only the orphan's runner 1", gh.removed)
	}
}

func TestReapAdoptsSecretOfExistingVM(t *testing.T) {
	// The scaler stopped after creating the VMI but before setting the Secret's owner;
	// the VM may still be booting and need its user-data.
	v := agedVMI("booting", "1", kubevirtv1.Scheduling, 5*time.Minute)
	v.UID = "vmi-uid"
	s, gh := newScaler(t, v, agedSecret(vm.SecretName("booting"), "1", 5*time.Minute, false))
	s.Reap(context.Background())

	var sec corev1.Secret
	if !exists(t, s, &sec, vm.SecretName("booting")) {
		t.Fatal("secret of an existing VM deleted")
	}
	if len(sec.OwnerReferences) != 1 || sec.OwnerReferences[0].UID != "vmi-uid" {
		t.Errorf("owners = %+v, want the VMI", sec.OwnerReferences)
	}
	if len(gh.removed) != 0 {
		t.Errorf("removed runners %v, want none", gh.removed)
	}
}

func TestReapKeepsOrphanSecretWhenRunnerRemovalFails(t *testing.T) {
	// The Secret is the only record of the runner ID: keep it until removal works.
	s, gh := newScaler(t, agedSecret("orphan-userdata", "1", 10*time.Minute, false))
	gh.busy = map[int64]error{1: context.DeadlineExceeded}
	s.Reap(context.Background())
	if !exists(t, s, &corev1.Secret{}, "orphan-userdata") {
		t.Error("secret deleted although its runner is still registered")
	}
}

func TestReapRecordsPhasesAndReasons(t *testing.T) {
	s, _ := newScaler(t,
		agedVMI("done", "1", kubevirtv1.Succeeded, time.Minute),
		agedVMI("idle", "2", kubevirtv1.Running, time.Hour),
		agedVMI("working", "3", kubevirtv1.Running, time.Minute),
	)
	s.Reap(context.Background())
	out := scrapeScaler(t, s)
	for _, want := range []string{
		`kubevirt_scaleset_vms{name="homelab-runners-vm",phase="Running"} 2`,
		`kubevirt_scaleset_vms{name="homelab-runners-vm",phase="Succeeded"} 1`,
		`kubevirt_scaleset_reaped_vms_total{name="homelab-runners-vm",reason="finished"} 1`,
		`kubevirt_scaleset_reaped_vms_total{name="homelab-runners-vm",reason="idle"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s", want)
		}
	}
}
