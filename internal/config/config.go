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

	LogLevel string
}

// FromEnv reads the configuration and reports every problem at once.
func FromEnv() (Config, error) {
	var errs []error

	c := Config{
		GitHubConfigURL: env("GITHUB_CONFIG_URL", "https://github.com/milanoid-labs"),
		ScaleSetName:    env("SCALE_SET_NAME", "homelab-runners-vm"),
		RunnerGroup:     env("RUNNER_GROUP", "default"),
		AppClientID:     os.Getenv("GITHUB_APP_CLIENT_ID"),
		LogLevel:        env("LOG_LEVEL", "info"),
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
