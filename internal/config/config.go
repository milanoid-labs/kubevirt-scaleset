// Package config reads the scaler's settings from environment variables.
//
// Environment variables rather than flags: in Kubernetes they come straight from the
// Deployment and its Secret, and locally from direnv. The private key is read from a file
// (a mounted Secret), so it never sits in the process environment.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation"
)

type Config struct {
	// GitHubConfigURL is where the scale set is registered: an org URL for org runners.
	GitHubConfigURL string
	// ScaleSetName is also the only label: workflows use `runs-on: <ScaleSetName>`.
	ScaleSetName string
	RunnerGroup  string

	AppClientID       string // Client ID or App ID, the library accepts both
	AppInstallationID int64
	AppPrivateKey     string // PEM contents, read from AppPrivateKeyFile

	MinRunners int
	MaxRunners int

	// Namespace holds the runner VMIs and their Secrets.
	Namespace string
	VMImage   string // containerDisk, pinned by digest
	VMCores   uint32
	VMMemory  resource.Quantity

	// IdleTimeout: a runner VM that hasn't started a job this long after its creation is
	// removed (stuck boot, failing image pull, or its job went to another runner).
	IdleTimeout time.Duration
}

// FromEnv reads the configuration and reports every problem at once.
func FromEnv() (Config, error) {
	var errs []error

	c := Config{
		GitHubConfigURL: env("GITHUB_CONFIG_URL", "https://github.com/milanoid-labs"),
		ScaleSetName:    env("SCALE_SET_NAME", "homelab-runners-vm"),
		RunnerGroup:     env("RUNNER_GROUP", "default"),
		Namespace:       env("NAMESPACE", "kubevirt-runners"),
		VMImage:         os.Getenv("VM_IMAGE"),
		AppClientID:     os.Getenv("GITHUB_APP_CLIENT_ID"),
	}
	if c.AppClientID == "" {
		errs = append(errs, errors.New("GITHUB_APP_CLIENT_ID is required"))
	}

	var err error
	if c.AppInstallationID, err = strconv.ParseInt(os.Getenv("GITHUB_APP_INSTALLATION_ID"), 10, 64); err != nil {
		errs = append(errs, fmt.Errorf("GITHUB_APP_INSTALLATION_ID: %w", err))
	}

	if path := os.Getenv("GITHUB_APP_PRIVATE_KEY_FILE"); path == "" {
		errs = append(errs, errors.New("GITHUB_APP_PRIVATE_KEY_FILE is required"))
	} else if key, err := os.ReadFile(path); err != nil {
		errs = append(errs, fmt.Errorf("GITHUB_APP_PRIVATE_KEY_FILE: %w", err))
	} else {
		c.AppPrivateKey = string(key)
	}

	if c.MinRunners, err = intEnv("MIN_RUNNERS", 0); err != nil {
		errs = append(errs, err)
	}
	if c.MaxRunners, err = intEnv("MAX_RUNNERS", 2); err != nil {
		errs = append(errs, err)
	}
	if c.MinRunners < 0 || c.MaxRunners < 1 || c.MinRunners > c.MaxRunners {
		errs = append(errs, fmt.Errorf("need 0 <= MIN_RUNNERS (%d) <= MAX_RUNNERS (%d), MAX_RUNNERS >= 1", c.MinRunners, c.MaxRunners))
	}

	// The name prefixes VMI and Secret names (<name>-xxxxxxxx[-userdata]) and is a label
	// value: a DNS label of up to 63 characters covers both.
	for _, msg := range validation.IsDNS1123Label(c.ScaleSetName + "-xxxxxxxx") {
		errs = append(errs, fmt.Errorf("SCALE_SET_NAME %q (lowercase, digits and -, at most 54 characters): %s", c.ScaleSetName, msg))
	}

	if c.VMImage == "" {
		errs = append(errs, errors.New("VM_IMAGE is required"))
	}
	cores, err := intEnv("VM_CPU_CORES", 2)
	if err != nil || cores < 1 {
		errs = append(errs, fmt.Errorf("VM_CPU_CORES must be a positive number: %v", err))
	}
	c.VMCores = uint32(cores)
	if c.VMMemory, err = resource.ParseQuantity(env("VM_MEMORY", "4Gi")); err != nil {
		errs = append(errs, fmt.Errorf("VM_MEMORY: %w", err))
	} else if c.VMMemory.Sign() <= 0 {
		errs = append(errs, fmt.Errorf("VM_MEMORY must be positive, got %s", c.VMMemory.String()))
	}
	if c.IdleTimeout, err = time.ParseDuration(env("RUNNER_IDLE_TIMEOUT", "15m")); err != nil || c.IdleTimeout <= 0 {
		errs = append(errs, fmt.Errorf("RUNNER_IDLE_TIMEOUT must be a positive duration like 15m: %v", err))
	}

	return c, errors.Join(errs...)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func intEnv(key string, def int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return n, nil
}
