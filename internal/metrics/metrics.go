// Package metrics exports the scaler's Prometheus metrics on /metrics.
//
// The gha_* metrics copy the names, labels and buckets of ARC's listener
// (actions-runner-controller cmd/ghalistener/metrics), so the existing "ARC runners"
// dashboard reads pod and VM runners alike. Unlike ARC, every metric, the job ones too,
// carries name=<scale set>, so the dashboard can tell scale sets apart.
// kubevirt_scaleset_* are the VM-specific ones.
package metrics

import (
	"net/http"
	"time"

	"github.com/actions/scaleset"
	"github.com/actions/scaleset/listener"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// runtimeBuckets are ARC's defaultRuntimeBuckets: the same le values let one
// histogram_quantile sum over both runner pools.
var runtimeBuckets = []float64{
	0.01, 0.05, 0.1, 0.5, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 12, 15, 18, 20, 25, 30, 40, 50, 60,
	70, 80, 90, 100, 110, 120, 150, 180, 210, 240, 300, 360, 420, 480, 540, 600, 900, 1200,
	1800, 2400, 3000, 3600,
}

var (
	jobLabels          = []string{"organization", "repository", "job_name", "event_name"}
	completedJobLabels = append(jobLabels[:len(jobLabels):len(jobLabels)], "job_result")
)

// Recorder holds all metrics. It implements listener.MetricsRecorder for the gha_* ones.
type Recorder struct {
	reg *prometheus.Registry

	assignedJobs, runningJobs, availableJobs    prometheus.Gauge
	registeredRunners, busyRunners, idleRunners prometheus.Gauge
	desiredRunners, minRunners, maxRunners      prometheus.Gauge
	startedJobs, completedJobs                  *prometheus.CounterVec
	jobStartupDuration, jobExecutionDuration    *prometheus.HistogramVec
	vmBootDuration                              prometheus.Observer
	vms                                         *prometheus.GaugeVec
	reapedVMs, errors                           *prometheus.CounterVec
}

var _ listener.MetricsRecorder = (*Recorder)(nil)

// New registers the metrics of scale set name, with its fixed runner limits.
func New(name string, minRunners, maxRunners int) *Recorder {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	f := factory{prometheus.WrapRegistererWith(prometheus.Labels{"name": name}, reg)}

	r := &Recorder{
		reg:               reg,
		assignedJobs:      f.gauge("gha_assigned_jobs", "Number of jobs assigned to this scale set."),
		runningJobs:       f.gauge("gha_running_jobs", "Number of jobs running (or about to be run)."),
		availableJobs:     f.gauge("gha_available_jobs", "Number of jobs with runs-on matching the scale set, not yet assigned."),
		registeredRunners: f.gauge("gha_registered_runners", "Number of runners registered by the scale set."),
		busyRunners:       f.gauge("gha_busy_runners", "Number of registered runners running a job."),
		idleRunners:       f.gauge("gha_idle_runners", "Number of registered runners not running a job."),
		desiredRunners:    f.gauge("gha_desired_runners", "Number of runners desired by the scale set."),
		minRunners:        f.gauge("gha_min_runners", "Minimum number of runners."),
		maxRunners:        f.gauge("gha_max_runners", "Maximum number of runners."),
		startedJobs:       f.counterVec("gha_started_jobs_total", "Total number of jobs started.", jobLabels),
		completedJobs:     f.counterVec("gha_completed_jobs_total", "Total number of jobs completed.", completedJobLabels),
		jobStartupDuration: f.histogramVec("gha_job_startup_duration_seconds",
			"Time from the job's assignment to the scale set until a runner took it (in seconds).", jobLabels, runtimeBuckets),
		jobExecutionDuration: f.histogramVec("gha_job_execution_duration_seconds",
			"Time spent executing workflow jobs by the scale set (in seconds).", completedJobLabels, runtimeBuckets),

		vmBootDuration: f.histogramVec("kubevirt_scaleset_vm_boot_duration_seconds",
			"Time from creating a runner VM until its runner took a job (in seconds): boot plus runner start for a VM created for a waiting job.",
			nil, prometheus.ExponentialBucketsRange(10, 1200, 15)).WithLabelValues(),
		vms: f.gaugeVec("kubevirt_scaleset_vms", "Runner VMs by VMI phase, as the reaper last saw them.", "phase"),
		reapedVMs: f.counterVec("kubevirt_scaleset_reaped_vms_total",
			"Runner VMs deleted by the reaper, by reason (finished, idle).", []string{"reason"}),
		errors: f.counterVec("kubevirt_scaleset_errors_total",
			"Errors the scaler logged and retries later, by operation.", []string{"operation"}),
	}
	r.minRunners.Set(float64(minRunners))
	r.maxRunners.Set(float64(maxRunners))
	return r
}

// Handler serves the metrics.
func (r *Recorder) Handler() http.Handler {
	return promhttp.HandlerFor(r.reg, promhttp.HandlerOpts{Registry: r.reg})
}

func (r *Recorder) RecordStatistics(s *scaleset.RunnerScaleSetStatistic) {
	r.assignedJobs.Set(float64(s.TotalAssignedJobs))
	r.runningJobs.Set(float64(s.TotalRunningJobs))
	r.availableJobs.Set(float64(s.TotalAvailableJobs))
	r.registeredRunners.Set(float64(s.TotalRegisteredRunners))
	r.busyRunners.Set(float64(s.TotalBusyRunners))
	r.idleRunners.Set(float64(s.TotalIdleRunners))
}

func (r *Recorder) RecordDesiredRunners(count int) { r.desiredRunners.Set(float64(count)) }

// RecordJobStarted and RecordJobCompleted follow ARC: whole seconds, and no duration when
// a timestamp is missing or out of order.
func (r *Recorder) RecordJobStarted(m *scaleset.JobStarted) {
	if m.RunnerAssignTime.IsZero() {
		return
	}
	l := labelValues(&m.JobMessageBase)
	r.startedJobs.WithLabelValues(l...).Inc()
	if !m.ScaleSetAssignTime.IsZero() && !m.RunnerAssignTime.Before(m.ScaleSetAssignTime) {
		r.jobStartupDuration.WithLabelValues(l...).Observe(seconds(m.ScaleSetAssignTime, m.RunnerAssignTime))
	}
}

func (r *Recorder) RecordJobCompleted(m *scaleset.JobCompleted) {
	if m.RunnerAssignTime.IsZero() {
		return
	}
	l := append(labelValues(&m.JobMessageBase), m.Result)
	r.completedJobs.WithLabelValues(l...).Inc()
	if !m.FinishTime.IsZero() && !m.FinishTime.Before(m.RunnerAssignTime) {
		r.jobExecutionDuration.WithLabelValues(l...).Observe(seconds(m.RunnerAssignTime, m.FinishTime))
	}
}

// VMBooted records a runner VM's time from creation to its first job.
func (r *Recorder) VMBooted(d time.Duration) { r.vmBootDuration.Observe(d.Seconds()) }

// SetVMs replaces the VMs-by-phase gauge; phases missing from count drop out.
func (r *Recorder) SetVMs(count map[string]int) {
	r.vms.Reset()
	for phase, n := range count {
		r.vms.WithLabelValues(phase).Set(float64(n))
	}
}

func (r *Recorder) VMReaped(reason string) { r.reapedVMs.WithLabelValues(reason).Inc() }

// Error counts an error that was logged, not returned (the scaler retries on its own).
func (r *Recorder) Error(operation string) { r.errors.WithLabelValues(operation).Inc() }

func labelValues(m *scaleset.JobMessageBase) []string {
	return []string{m.OwnerName, m.RepositoryName, m.JobDisplayName, m.EventName}
}

func seconds(from, to time.Time) float64 { return float64(to.Unix() - from.Unix()) }

// factory creates and registers metrics in one go.
type factory struct{ reg prometheus.Registerer }

func (f factory) gauge(name, help string) prometheus.Gauge {
	g := prometheus.NewGauge(prometheus.GaugeOpts{Name: name, Help: help})
	f.reg.MustRegister(g)
	return g
}

func (f factory) gaugeVec(name, help string, labels ...string) *prometheus.GaugeVec {
	g := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: name, Help: help}, labels)
	f.reg.MustRegister(g)
	return g
}

func (f factory) counterVec(name, help string, labels []string) *prometheus.CounterVec {
	c := prometheus.NewCounterVec(prometheus.CounterOpts{Name: name, Help: help}, labels)
	f.reg.MustRegister(c)
	return c
}

func (f factory) histogramVec(name, help string, labels []string, buckets []float64) *prometheus.HistogramVec {
	h := prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: name, Help: help, Buckets: buckets}, labels)
	f.reg.MustRegister(h)
	return h
}
