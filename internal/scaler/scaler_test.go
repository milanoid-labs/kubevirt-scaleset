package scaler

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/actions/scaleset"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clienttesting "k8s.io/client-go/testing"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/milanoid-labs/kubevirt-scaleset/internal/vm"
)

type fakeJIT struct{ names []string }

func (f *fakeJIT) GenerateJitRunnerConfig(_ context.Context, s *scaleset.RunnerScaleSetJitRunnerSetting, _ int) (*scaleset.RunnerScaleSetJitRunnerConfig, error) {
	f.names = append(f.names, s.Name)
	return &scaleset.RunnerScaleSetJitRunnerConfig{EncodedJITConfig: "jit-for-" + s.Name}, nil
}

func newScaler(t *testing.T, objs ...client.Object) (*Scaler, *fakeJIT) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kubevirtv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	jit := &fakeJIT{}
	return &Scaler{
		// A plain object tracker: the default one tracks managed fields and panics on
		// KubeVirt's uint64 fields (a fake-client limitation, the API server is fine).
		Kube: fake.NewClientBuilder().WithScheme(scheme).
			WithObjectTracker(clienttesting.NewObjectTracker(scheme, serializer.NewCodecFactory(scheme).UniversalDecoder())).
			WithObjects(objs...).Build(),
		GitHub:     jit,
		ScaleSetID: 3,
		VM: vm.Spec{
			Namespace: "runners", ScaleSet: "homelab-runners-vm",
			Image: "registry/vm@sha256:abc", CPUCores: 2, Memory: resource.MustParse("4Gi"),
		},
		MinRunners: 0,
		MaxRunners: 2,
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, jit
}

// existingVMI is a VMI of the scale set in the given phase.
func existingVMI(name string, phase kubevirtv1.VirtualMachineInstancePhase) *kubevirtv1.VirtualMachineInstance {
	return &kubevirtv1.VirtualMachineInstance{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "runners", Labels: map[string]string{
			vm.ManagedByLabel: vm.ManagedBy, vm.ScaleSetLabel: "homelab-runners-vm",
		}},
		Status: kubevirtv1.VirtualMachineInstanceStatus{Phase: phase},
	}
}

func listVMIs(t *testing.T, s *Scaler) []kubevirtv1.VirtualMachineInstance {
	t.Helper()
	var l kubevirtv1.VirtualMachineInstanceList
	if err := s.Kube.List(context.Background(), &l, client.InNamespace("runners")); err != nil {
		t.Fatal(err)
	}
	return l.Items
}

func TestScaleUpCreatesVMIAndOwnedSecret(t *testing.T) {
	s, jit := newScaler(t)

	got, err := s.HandleDesiredRunnerCount(context.Background(), 1)
	if err != nil || got != 1 {
		t.Fatalf("got %d, %v; want 1, nil", got, err)
	}

	vmis := listVMIs(t, s)
	if len(vmis) != 1 {
		t.Fatalf("want 1 VMI, got %d", len(vmis))
	}
	v := vmis[0]
	if v.Name != jit.names[0] || !strings.HasPrefix(v.Name, "homelab-runners-vm-") {
		t.Errorf("VMI %q, runner %q: want the same name with the scale set prefix", v.Name, jit.names[0])
	}
	if v.Spec.Volumes[0].ContainerDisk.Image != "registry/vm@sha256:abc" {
		t.Errorf("image = %q", v.Spec.Volumes[0].ContainerDisk.Image)
	}

	var sec corev1.Secret
	if err := s.Kube.Get(context.Background(), client.ObjectKey{Namespace: "runners", Name: vm.SecretName(v.Name)}, &sec); err != nil {
		t.Fatal(err)
	}
	// The fake client keeps StringData as given; a real API server moves it into Data.
	ud := sec.StringData["userdata"] + string(sec.Data["userdata"])
	if !strings.Contains(ud, "content: jit-for-"+v.Name) || !strings.Contains(ud, "actions-runner.service") {
		t.Errorf("user-data does not follow the boot contract:\n%s", ud)
	}
	if len(sec.OwnerReferences) != 1 || sec.OwnerReferences[0].Name != v.Name || sec.OwnerReferences[0].Kind != "VirtualMachineInstance" {
		t.Errorf("secret owners = %+v, want the VMI", sec.OwnerReferences)
	}
}

func TestScaleUpIsCappedByMaxRunners(t *testing.T) {
	s, _ := newScaler(t)
	if got, _ := s.HandleDesiredRunnerCount(context.Background(), 5); got != 2 {
		t.Fatalf("got %d, want MaxRunners 2", got)
	}
	if n := len(listVMIs(t, s)); n != 2 {
		t.Fatalf("want 2 VMIs, got %d", n)
	}
}

func TestExistingVMIsCountUnlessFinished(t *testing.T) {
	// One running (counts), one Succeeded (job done, waiting for the reaper: doesn't count).
	s, jit := newScaler(t,
		existingVMI("homelab-runners-vm-running", kubevirtv1.Running),
		existingVMI("homelab-runners-vm-done", kubevirtv1.Succeeded),
	)
	got, _ := s.HandleDesiredRunnerCount(context.Background(), 2)
	if got != 2 || len(jit.names) != 1 {
		t.Fatalf("got %d with %d new runners; want 2 with 1 new", got, len(jit.names))
	}
}

func TestNoJobsNoVMs(t *testing.T) {
	s, jit := newScaler(t)
	if got, _ := s.HandleDesiredRunnerCount(context.Background(), 0); got != 0 || len(jit.names) != 0 {
		t.Fatalf("got %d, %d runners; want none", got, len(jit.names))
	}
}

func TestMinRunnersKeepsWarmVMs(t *testing.T) {
	s, _ := newScaler(t)
	s.MinRunners = 1
	if got, _ := s.HandleDesiredRunnerCount(context.Background(), 0); got != 1 {
		t.Fatalf("got %d, want MinRunners 1", got)
	}
}
