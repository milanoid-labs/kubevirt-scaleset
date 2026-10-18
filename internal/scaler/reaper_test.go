package scaler

import (
	"context"
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

func TestReapOrphanSecrets(t *testing.T) {
	secret := func(name string, age time.Duration, owned bool) *corev1.Secret {
		s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "runners", CreationTimestamp: metav1.NewTime(time.Now().Add(-age)),
			Labels: map[string]string{vm.ManagedByLabel: vm.ManagedBy, vm.ScaleSetLabel: "homelab-runners-vm"},
		}}
		if owned {
			s.OwnerReferences = []metav1.OwnerReference{{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachineInstance", Name: "x", UID: "u"}}
		}
		return s
	}
	s, _ := newScaler(t,
		secret("orphan", 10*time.Minute, false),
		secret("just-created", 10*time.Second, false),
		secret("owned", 10*time.Minute, true),
	)
	s.Reap(context.Background())

	for name, want := range map[string]bool{"orphan": false, "just-created": true, "owned": true} {
		if got := exists(t, s, &corev1.Secret{}, name); got != want {
			t.Errorf("secret %s exists = %v, want %v", name, got, want)
		}
	}
}
