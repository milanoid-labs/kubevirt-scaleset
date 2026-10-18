// Package scaler turns GitHub's job count into runner VMs. It implements the
// actions/scaleset listener's Scaler interface.
package scaler

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/actions/scaleset"
	"github.com/actions/scaleset/listener"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/milanoid-labs/kubevirt-scaleset/internal/vm"
)

// Runners is the part of the scaleset client the scaler needs (a fake in tests).
type Runners interface {
	GenerateJitRunnerConfig(ctx context.Context, setting *scaleset.RunnerScaleSetJitRunnerSetting, scaleSetID int) (*scaleset.RunnerScaleSetJitRunnerConfig, error)
	RemoveRunner(ctx context.Context, runnerID int64) error
}

type Scaler struct {
	Kube       client.Client
	GitHub     Runners
	ScaleSetID int
	VM         vm.Spec
	MinRunners int
	MaxRunners int
	// IdleTimeout is how long a VM may exist without running a job (boot included)
	// before the reaper removes it.
	IdleTimeout time.Duration
	Log         *slog.Logger
}

var _ listener.Scaler = (*Scaler)(nil)

// HandleDesiredRunnerCount makes sure min(MaxRunners, MinRunners+assigned) VMs exist.
//
// The VMIs in the cluster are the state, not memory, so a restarted scaler neither loses
// nor duplicates VMs. Errors are logged, not returned: the listener would end the session
// on an error, and it calls again within ~50 s anyway, which is the retry.
// Scaling down needs nothing here: each VM powers itself off after its one job.
func (s *Scaler) HandleDesiredRunnerCount(ctx context.Context, assigned int) (int, error) {
	current, err := s.countActive(ctx)
	if err != nil {
		s.Log.Error("listing runner VMs", "error", err)
		return 0, nil
	}
	desired := min(s.MaxRunners, s.MinRunners+assigned)
	if desired <= current {
		return current, nil
	}

	s.Log.Info("scaling up", "assignedJobs", assigned, "current", current, "desired", desired)
	for range desired - current {
		name, err := s.createRunner(ctx)
		if err != nil {
			s.Log.Error("creating runner VM", "error", err)
			break
		}
		current++
		s.Log.Info("created runner VM", "name", name)
	}
	return current, nil
}

func (s *Scaler) HandleJobStarted(_ context.Context, job *scaleset.JobStarted) error {
	s.Log.Info("job started", "runner", job.RunnerName, "repository", job.RepositoryName,
		"job", job.JobDisplayName, "workflowRunID", job.WorkflowRunID)
	return nil
}

// HandleJobCompleted only logs: the VM powers itself off, and the reaper deletes it.
func (s *Scaler) HandleJobCompleted(_ context.Context, job *scaleset.JobCompleted) error {
	s.Log.Info("job completed", "runner", job.RunnerName, "repository", job.RepositoryName,
		"job", job.JobDisplayName, "result", job.Result)
	return nil
}

// countActive counts this scale set's VMIs that may still run a job: everything but
// Succeeded/Failed (finished, waiting for the reaper).
func (s *Scaler) countActive(ctx context.Context) (int, error) {
	var vmis kubevirtv1.VirtualMachineInstanceList
	if err := s.Kube.List(ctx, &vmis, s.owned()...); err != nil {
		return 0, err
	}
	n := 0
	for _, v := range vmis.Items {
		if v.Status.Phase != kubevirtv1.Succeeded && v.Status.Phase != kubevirtv1.Failed {
			n++
		}
	}
	return n, nil
}

// createRunner registers a JIT runner and boots a VM for it: Secret first (the VMI needs
// it at boot), then the VMI, then the VMI becomes the Secret's owner, so Kubernetes'
// garbage collector deletes the Secret together with the VMI.
func (s *Scaler) createRunner(ctx context.Context) (string, error) {
	// Runner name = VMI name, so a GitHub runner and its VM are easy to match.
	name := s.VM.ScaleSet + "-" + strings.ToLower(rand.Text()[:8])

	jit, err := s.GitHub.GenerateJitRunnerConfig(ctx, &scaleset.RunnerScaleSetJitRunnerSetting{
		Name: name, WorkFolder: "_work",
	}, s.ScaleSetID)
	if err != nil {
		return "", fmt.Errorf("JIT config for %s: %w", name, err)
	}

	secret := s.VM.Secret(name, jit.EncodedJITConfig)
	if err := s.Kube.Create(ctx, secret); err != nil {
		s.removeUnusedRunner(ctx, name, jit.Runner)
		return "", fmt.Errorf("create secret %s: %w", secret.Name, err)
	}
	vmi := s.VM.VMI(name)
	if jit.Runner != nil {
		vmi.Annotations = map[string]string{vm.RunnerIDAnnotation: strconv.Itoa(jit.Runner.ID)}
	}
	if err := s.Kube.Create(ctx, vmi); err != nil {
		if derr := s.Kube.Delete(ctx, secret); derr != nil {
			s.Log.Warn("deleting unused secret", "name", secret.Name, "error", derr)
		}
		s.removeUnusedRunner(ctx, name, jit.Runner)
		return "", fmt.Errorf("create VMI %s: %w", name, err)
	}

	// A patch with only the owner reference; the JIT config is not sent again.
	secret.StringData = nil
	patch := client.MergeFrom(secret.DeepCopy())
	if err := controllerutil.SetOwnerReference(vmi, secret, s.Kube.Scheme()); err != nil {
		return "", fmt.Errorf("owner reference on %s: %w", secret.Name, err)
	}
	if err := s.Kube.Patch(ctx, secret, patch); err != nil {
		return "", fmt.Errorf("set owner of %s: %w", secret.Name, err)
	}
	return name, nil
}

// owned selects this scale set's objects in the namespace.
func (s *Scaler) owned() []client.ListOption {
	return []client.ListOption{client.InNamespace(s.VM.Namespace), client.MatchingLabels{
		vm.ManagedByLabel: vm.ManagedBy, vm.ScaleSetLabel: s.VM.ScaleSet,
	}}
}

// removeUnusedRunner unregisters a JIT runner whose VM was never created, so it doesn't
// linger as an offline runner in GitHub.
func (s *Scaler) removeUnusedRunner(ctx context.Context, name string, r *scaleset.RunnerReference) {
	if r == nil {
		return
	}
	if err := s.GitHub.RemoveRunner(ctx, int64(r.ID)); err != nil && !errors.Is(err, scaleset.RunnerNotFoundError) {
		s.Log.Warn("removing unused runner", "name", name, "error", err)
	}
}
