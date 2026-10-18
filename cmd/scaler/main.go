// Command scaler runs GitHub Actions jobs in KubeVirt VMs: one ephemeral VM per job.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/actions/scaleset/listener"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	kubevirtv1 "kubevirt.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/milanoid-labs/kubevirt-scaleset/internal/config"
	"github.com/milanoid-labs/kubevirt-scaleset/internal/github"
	"github.com/milanoid-labs/kubevirt-scaleset/internal/metrics"
	"github.com/milanoid-labs/kubevirt-scaleset/internal/scaler"
	"github.com/milanoid-labs/kubevirt-scaleset/internal/vm"
)

// version is set at build time: -ldflags "-X main.version=..."
var version = "dev"

func main() {
	// JSON logging from the first line, so startup failures (config, auth) are JSON too.
	log := newLogger(os.Getenv("LOG_LEVEL"))
	slog.SetDefault(log)
	if err := run(log); err != nil {
		log.Error("scaler failed", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.FromEnv()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	log.Info("starting", "version", version, "url", cfg.GitHubConfigURL, "scaleSet", cfg.ScaleSetName,
		"minRunners", cfg.MinRunners, "maxRunners", cfg.MaxRunners, "namespace", cfg.Namespace, "image", cfg.VMImage,
		"idleTimeout", cfg.IdleTimeout)

	// SIGTERM is what Kubernetes sends on pod shutdown, SIGINT is Ctrl-C locally.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	gh, err := github.NewClient(cfg.GitHubConfigURL, cfg.AppClientID, cfg.AppInstallationID, cfg.AppPrivateKey, version)
	if err != nil {
		return fmt.Errorf("github client: %w", err)
	}

	scaleSet, err := github.EnsureScaleSet(ctx, gh, cfg.RunnerGroup, cfg.ScaleSetName, log)
	if err != nil {
		return err
	}

	log.Info("scale set ready", "id", scaleSet.ID)

	// Metrics first: the listener records into it from its first message.
	rec := metrics.New(cfg.ScaleSetName, cfg.MinRunners, cfg.MaxRunners)
	if err := serveMetrics(ctx, cfg.MetricsAddr, rec, log); err != nil {
		return fmt.Errorf("metrics server: %w", err)
	}

	// Kubernetes client: in the pod from its ServiceAccount, locally from KUBECONFIG.
	kube, err := newKubeClient()
	if err != nil {
		return fmt.Errorf("kubernetes client: %w", err)
	}

	// One session per scale set at a time: GitHub rejects a second listener. The owner
	// name shows up in GitHub's logs; the pod name in the cluster.
	owner, _ := os.Hostname()
	session, err := gh.MessageSessionClient(ctx, scaleSet.ID, owner)
	if err != nil {
		return fmt.Errorf("message session: %w", err)
	}
	defer session.Close(context.WithoutCancel(ctx))

	l, err := listener.New(session, listener.Config{
		ScaleSetID: scaleSet.ID,
		MaxRunners: cfg.MaxRunners,
		Logger:     log.WithGroup("listener"),
	}, listener.WithMetricsRecorder(rec))
	if err != nil {
		return fmt.Errorf("listener: %w", err)
	}

	s := &scaler.Scaler{
		Kube:       kube,
		GitHub:     gh,
		ScaleSetID: scaleSet.ID,
		VM: vm.Spec{
			Namespace: cfg.Namespace,
			ScaleSet:  cfg.ScaleSetName,
			Image:     cfg.VMImage,
			CPUCores:  cfg.VMCores,
			Memory:    cfg.VMMemory,
		},
		MinRunners:  cfg.MinRunners,
		MaxRunners:  cfg.MaxRunners,
		IdleTimeout: cfg.IdleTimeout,
		Metrics:     rec,
		Log:         log.WithGroup("scaler"),
	}

	// The reaper runs beside the listener and stops with it (same ctx).
	go s.RunReaper(ctx, 30*time.Second)

	log.Info("listening for jobs")
	if err := l.Run(ctx, s); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("listener: %w", err)
	}
	log.Info("stopped")
	return nil
}

// serveMetrics serves /metrics and /healthz until ctx ends. The port is bound here, so a
// taken port fails startup instead of a background goroutine.
func serveMetrics(ctx context.Context, addr string, rec *metrics.Recorder, log *slog.Logger) error {
	mux := http.NewServeMux()
	mux.Handle("/metrics", rec.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok\n")) })

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("metrics server", "error", err)
		}
	}()
	go func() {
		<-ctx.Done()
		srv.Shutdown(context.WithoutCancel(ctx))
	}()
	log.Info("serving metrics", "addr", ln.Addr().String())
	return nil
}

func newKubeClient() (client.Client, error) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, err
	}
	if err := kubevirtv1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	cfg, err := ctrl.GetConfig()
	if err != nil {
		return nil, err
	}
	return client.New(cfg, client.Options{Scheme: scheme})
}

// newLogger logs JSON, which Loki and kubectl logs | jq both read well. LOG_LEVEL is
// debug, info (default), warn or error.
func newLogger(level string) *slog.Logger {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		l = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
}
