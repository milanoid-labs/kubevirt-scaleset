package metrics

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/actions/scaleset"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func scrape(t *testing.T, r *Recorder) string {
	t.Helper()
	w := httptest.NewRecorder()
	r.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	return w.Body.String()
}

func job(assigned, started, finished time.Time) scaleset.JobMessageBase {
	return scaleset.JobMessageBase{
		OwnerName: "milanoid-labs", RepositoryName: "kubevirt-scaleset", JobDisplayName: "smoke",
		EventName: "workflow_dispatch", ScaleSetAssignTime: assigned, RunnerAssignTime: started, FinishTime: finished,
	}
}

func TestGaugesCarryTheScaleSetName(t *testing.T) {
	r := New("homelab-runners-vm", 0, 2)
	r.RecordStatistics(&scaleset.RunnerScaleSetStatistic{TotalAssignedJobs: 3, TotalRunningJobs: 1, TotalBusyRunners: 1})
	r.RecordDesiredRunners(2)

	out := scrape(t, r)
	for _, want := range []string{
		`gha_assigned_jobs{name="homelab-runners-vm"} 3`,
		`gha_running_jobs{name="homelab-runners-vm"} 1`,
		`gha_busy_runners{name="homelab-runners-vm"} 1`,
		`gha_desired_runners{name="homelab-runners-vm"} 2`,
		`gha_max_runners{name="homelab-runners-vm"} 2`,
		`gha_min_runners{name="homelab-runners-vm"} 0`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s", want)
		}
	}
}

func TestJobMetricsLikeARC(t *testing.T) {
	r := New("homelab-runners-vm", 0, 2)
	t0 := time.Date(2026, 10, 18, 15, 39, 13, 0, time.UTC)
	r.RecordJobStarted(&scaleset.JobStarted{JobMessageBase: job(t0, t0.Add(51*time.Second), time.Time{})})
	r.RecordJobCompleted(&scaleset.JobCompleted{Result: "succeeded", JobMessageBase: job(t0, t0.Add(51*time.Second), t0.Add(58*time.Second))})

	l := []string{"milanoid-labs", "kubevirt-scaleset", "smoke", "workflow_dispatch"}
	if got := testutil.ToFloat64(r.startedJobs.WithLabelValues(l...)); got != 1 {
		t.Errorf("started = %v, want 1", got)
	}
	if got := testutil.ToFloat64(r.completedJobs.WithLabelValues(append(l, "succeeded")...)); got != 1 {
		t.Errorf("completed = %v, want 1", got)
	}
	out := scrape(t, r)
	for _, want := range []string{
		// 51 s startup and 7 s execution: the sums, and the first bucket each falls in.
		`gha_job_startup_duration_seconds_sum{event_name="workflow_dispatch",job_name="smoke",name="homelab-runners-vm",organization="milanoid-labs",repository="kubevirt-scaleset"} 51`,
		`gha_job_startup_duration_seconds_bucket{event_name="workflow_dispatch",job_name="smoke",name="homelab-runners-vm",organization="milanoid-labs",repository="kubevirt-scaleset",le="60"} 1`,
		`gha_job_execution_duration_seconds_sum{event_name="workflow_dispatch",job_name="smoke",job_result="succeeded",name="homelab-runners-vm",organization="milanoid-labs",repository="kubevirt-scaleset"} 7`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s", want)
		}
	}
}

func TestJobWithoutRunnerAssignTimeIsSkipped(t *testing.T) {
	r := New("x", 0, 1)
	r.RecordJobStarted(&scaleset.JobStarted{JobMessageBase: job(time.Now(), time.Time{}, time.Time{})})
	if n := testutil.CollectAndCount(r.startedJobs); n != 0 {
		t.Errorf("%d started series, want none", n)
	}
}

func TestSetVMsDropsGonePhases(t *testing.T) {
	r := New("x", 0, 1)
	r.SetVMs(map[string]int{"Running": 2, "Succeeded": 1})
	r.SetVMs(map[string]int{"Running": 1})
	if got := testutil.ToFloat64(r.vms.WithLabelValues("Running")); got != 1 {
		t.Errorf("Running = %v, want 1", got)
	}
	if n := testutil.CollectAndCount(r.vms); n != 1 {
		t.Errorf("%d phase series, want only Running", n)
	}
}
