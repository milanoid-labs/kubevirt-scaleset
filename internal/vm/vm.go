// Package vm builds the Kubernetes objects for one runner VM: the cloud-init Secret
// carrying the JIT config, and the VirtualMachineInstance booting the runner image.
package vm

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubevirtv1 "kubevirt.io/api/core/v1"
)

const (
	// ManagedByLabel marks every object the scaler owns; it lists VMIs by this label.
	ManagedByLabel = "app.kubernetes.io/managed-by"
	ManagedBy      = "kubevirt-scaleset"
	// ScaleSetLabel tells VMs of different scale sets apart in the same namespace.
	ScaleSetLabel = "kubevirt-scaleset.milanoid.net/scale-set"
	// RunnerIDAnnotation is the GitHub runner ID of the VM, for removing its registration.
	RunnerIDAnnotation = "kubevirt-scaleset.milanoid.net/runner-id"

	// NodeLabel/NodeTaint reserve kubevirt01 for runner VMs (homelab-cluster CLAUDE.md).
	NodeLabel = "homelab.milanoid.net/kubevirt-runners"
)

// Spec is what every runner VM of a scale set has in common.
type Spec struct {
	Namespace string
	ScaleSet  string
	Image     string // containerDisk, ideally pinned by digest
	CPUCores  uint32
	Memory    resource.Quantity
}

// UserData is the boot contract (vm-image/README.md): cloud-init writes the JIT config
// root-only, then starts the runner unit, which powers the VM off when the runner exits.
func UserData(jitConfig string) string {
	return fmt.Sprintf(`#cloud-config
write_files:
  - path: /etc/actions-runner/jitconfig
    owner: root:root
    permissions: "0600"
    content: %s
runcmd:
  - [systemctl, start, --no-block, actions-runner.service]
`, jitConfig)
}

func (s Spec) labels() map[string]string {
	return map[string]string{ManagedByLabel: ManagedBy, ScaleSetLabel: s.ScaleSet}
}

// SecretName is the cloud-init Secret of VM name.
func SecretName(name string) string { return name + "-userdata" }

// Secret holds the user-data. The JIT config is a credential, so it never goes inline
// into the VMI spec, where anyone allowed to read VMIs would see it.
func (s Spec) Secret(name, jitConfig string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: SecretName(name), Namespace: s.Namespace, Labels: s.labels()},
		Type:       corev1.SecretTypeOpaque,
		StringData: map[string]string{"userdata": UserData(jitConfig)},
	}
}

// VMI is one runner VM; same shape as test/vm/vmi.yaml.
func (s Spec) VMI(name string) *kubevirtv1.VirtualMachineInstance {
	evictionNone := kubevirtv1.EvictionStrategyNone
	grace := int64(30)
	secureBootOff := false
	return &kubevirtv1.VirtualMachineInstance{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: s.Namespace, Labels: s.labels()},
		Spec: kubevirtv1.VirtualMachineInstanceSpec{
			// KubeVirt's workloads.nodePlacement only places virt-handler; each VMI needs its own.
			NodeSelector: map[string]string{NodeLabel: "true"},
			Tolerations: []corev1.Toleration{{
				Key: NodeLabel, Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule,
			}},
			EvictionStrategy:              &evictionNone, // a job VM is never migrated; it just ends
			TerminationGracePeriodSeconds: &grace,
			Domain: kubevirtv1.DomainSpec{
				// UEFI finds /boot by filesystem UUID; Secure Boot would also need SMM.
				Firmware: &kubevirtv1.Firmware{Bootloader: &kubevirtv1.Bootloader{
					EFI: &kubevirtv1.EFI{SecureBoot: &secureBootOff},
				}},
				CPU:    &kubevirtv1.CPU{Cores: s.CPUCores},
				Memory: &kubevirtv1.Memory{Guest: &s.Memory},
				Devices: kubevirtv1.Devices{
					Disks: []kubevirtv1.Disk{
						{Name: "root", DiskDevice: kubevirtv1.DiskDevice{Disk: &kubevirtv1.DiskTarget{Bus: kubevirtv1.DiskBusVirtio}}},
						{Name: "cloudinit", DiskDevice: kubevirtv1.DiskDevice{Disk: &kubevirtv1.DiskTarget{Bus: kubevirtv1.DiskBusVirtio}}},
					},
					Interfaces: []kubevirtv1.Interface{{
						Name:                   "default",
						InterfaceBindingMethod: kubevirtv1.InterfaceBindingMethod{Masquerade: &kubevirtv1.InterfaceMasquerade{}},
					}},
					Rng: &kubevirtv1.Rng{}, // entropy for TLS right after boot
				},
			},
			Networks: []kubevirtv1.Network{{
				Name: "default", NetworkSource: kubevirtv1.NetworkSource{Pod: &kubevirtv1.PodNetwork{}},
			}},
			Volumes: []kubevirtv1.Volume{
				{Name: "root", VolumeSource: kubevirtv1.VolumeSource{
					ContainerDisk: &kubevirtv1.ContainerDiskSource{Image: s.Image},
				}},
				{Name: "cloudinit", VolumeSource: kubevirtv1.VolumeSource{
					CloudInitNoCloud: &kubevirtv1.CloudInitNoCloudSource{
						UserDataSecretRef: &corev1.LocalObjectReference{Name: SecretName(name)},
					},
				}},
			},
		},
	}
}
