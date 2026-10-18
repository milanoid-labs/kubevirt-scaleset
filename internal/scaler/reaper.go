package scaler

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/actions/scaleset"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/milanoid-labs/kubevirt-scaleset/internal/vm"
)

// orphanSecretAge is how old a Secret without an owning VMI must be before it's deleted.
// createRunner sets the owner a moment after creating the Secret; this covers that window.
const orphanSecretAge = 2 * time.Minute

// RunReaper reaps once now and then every interval, until ctx ends.
func (s *Scaler) RunReaper(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		s.Reap(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Reap deletes the VMs that are done (Succeeded/Failed) or never ran a job within
// IdleTimeout (stuck booting, image pull failing, no job for it), and settles Secrets
// without an owner. A VMI's Secret goes with it: the VMI owns it.
//
// The runner registration is removed first, and GitHub refuses that while the runner runs
// a job (JobStillRunningError): this is what keeps a long job's VM safe from IdleTimeout,
// with no job state in the scaler. ARC's ephemeral runner controller does the same.
func (s *Scaler) Reap(ctx context.Context) {
	var vmis kubevirtv1.VirtualMachineInstanceList
	if err := s.Kube.List(ctx, &vmis, s.owned()...); err != nil {
		s.Log.Error("reaper: listing runner VMs", "error", err)
		return
	}
	for i := range vmis.Items {
		v := &vmis.Items[i]
		if v.DeletionTimestamp != nil {
			continue
		}
		switch {
		case v.Status.Phase == kubevirtv1.Succeeded || v.Status.Phase == kubevirtv1.Failed:
			s.reapVMI(ctx, v, "finished")
		case time.Since(v.CreationTimestamp.Time) > s.IdleTimeout:
			s.reapVMI(ctx, v, "idle")
		}
	}
	s.reapOrphanSecrets(ctx)
}

func (s *Scaler) reapVMI(ctx context.Context, v *kubevirtv1.VirtualMachineInstance, reason string) {
	log := s.Log.With("name", v.Name, "phase", v.Status.Phase, "reason", reason)
	if err := s.removeRunner(ctx, v); err != nil {
		if errors.Is(err, scaleset.JobStillRunningError) {
			log.Debug("runner is running a job, keeping its VM")
		} else {
			log.Error("reaper: removing runner registration", "error", err)
		}
		return
	}
	if err := s.Kube.Delete(ctx, v); client.IgnoreNotFound(err) != nil {
		log.Error("reaper: deleting runner VM", "error", err)
		return
	}
	log.Info("deleted runner VM")
}

// removeRunner unregisters the runner recorded on obj (a VMI or its Secret). Already gone
// (an ephemeral runner removes itself after its job) counts as done.
func (s *Scaler) removeRunner(ctx context.Context, obj client.Object) error {
	id, err := strconv.ParseInt(obj.GetAnnotations()[vm.RunnerIDAnnotation], 10, 64)
	if err != nil {
		s.Log.Warn("reaper: no runner ID, its registration is left to GitHub", "name", obj.GetName())
		return nil
	}
	if err := s.GitHub.RemoveRunner(ctx, id); err != nil && !errors.Is(err, scaleset.RunnerNotFoundError) {
		return err
	}
	return nil
}

// reapOrphanSecrets settles Secrets that never got their VMI as owner (the scaler stopped
// or failed between the steps of createRunner). If the VMI exists, it adopts the Secret: a
// VM still booting may need it. Otherwise it unregisters the runner, then deletes the
// Secret; if that fails, the Secret stays as the record and the next pass retries.
func (s *Scaler) reapOrphanSecrets(ctx context.Context) {
	var secrets corev1.SecretList
	if err := s.Kube.List(ctx, &secrets, s.owned()...); err != nil {
		s.Log.Error("reaper: listing secrets", "error", err)
		return
	}
	for i := range secrets.Items {
		sec := &secrets.Items[i]
		if len(sec.OwnerReferences) > 0 || time.Since(sec.CreationTimestamp.Time) < orphanSecretAge {
			continue
		}
		log := s.Log.With("name", sec.Name)

		var vmi kubevirtv1.VirtualMachineInstance
		err := s.Kube.Get(ctx, client.ObjectKey{Namespace: sec.Namespace, Name: vm.VMName(sec.Name)}, &vmi)
		switch {
		case err == nil:
			patch := client.MergeFrom(sec.DeepCopy())
			if err := controllerutil.SetOwnerReference(&vmi, sec, s.Kube.Scheme()); err != nil {
				log.Error("reaper: owner reference", "error", err)
			} else if err := s.Kube.Patch(ctx, sec, patch); err != nil {
				log.Error("reaper: adopting secret", "error", err)
			} else {
				log.Info("adopted secret by its VM")
			}
			continue
		case !apierrors.IsNotFound(err):
			log.Error("reaper: looking up the secret's VM", "error", err)
			continue
		}

		if err := s.removeRunner(ctx, sec); err != nil {
			log.Error("reaper: removing runner registration", "error", err)
			continue
		}
		if err := s.Kube.Delete(ctx, sec); client.IgnoreNotFound(err) != nil {
			log.Error("reaper: deleting orphan secret", "error", err)
			continue
		}
		log.Info("deleted orphan secret")
	}
}
