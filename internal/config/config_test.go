package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setRequired sets the variables without defaults; key is a throwaway file.
func setRequired(t *testing.T) {
	t.Helper()
	key := filepath.Join(t.TempDir(), "key.pem")
	if err := os.WriteFile(key, []byte("pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GITHUB_APP_CLIENT_ID", "1")
	t.Setenv("GITHUB_APP_INSTALLATION_ID", "2")
	t.Setenv("GITHUB_APP_PRIVATE_KEY_FILE", key)
	t.Setenv("VM_IMAGE", "registry/vm@sha256:abc")
}

func TestDefaultsAreValid(t *testing.T) {
	setRequired(t)
	if _, err := FromEnv(); err != nil {
		t.Fatal(err)
	}
}

func TestRejectsBadValues(t *testing.T) {
	for _, tc := range []struct{ key, value, want string }{
		{"SCALE_SET_NAME", "MyRunners", "SCALE_SET_NAME"},
		{"SCALE_SET_NAME", "runners_vm", "SCALE_SET_NAME"},
		{"SCALE_SET_NAME", strings.Repeat("a", 55), "SCALE_SET_NAME"},
		{"SCALE_SET_NAME", "runners-", "SCALE_SET_NAME"}, // valid with the suffix, not as a label value
		{"VM_MEMORY", "-1Gi", "VM_MEMORY"},
		{"VM_MEMORY", "0", "VM_MEMORY"},
		{"RUNNER_IDLE_TIMEOUT", "0s", "RUNNER_IDLE_TIMEOUT"},
	} {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			setRequired(t)
			t.Setenv(tc.key, tc.value)
			if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want one about %s", err, tc.want)
			}
		})
	}
}

func TestLongestScaleSetName(t *testing.T) {
	setRequired(t)
	t.Setenv("SCALE_SET_NAME", strings.Repeat("a", 54))
	if _, err := FromEnv(); err != nil {
		t.Fatal(err)
	}
}
