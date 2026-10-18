// Package github talks to GitHub through the actions/scaleset client.
package github

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/actions/scaleset"
)

// NewClient returns a scale set client authenticated as the GitHub App. The library swaps
// the private key for short-lived tokens and refreshes them itself.
func NewClient(configURL, clientID string, installationID int64, privateKey, version string) (*scaleset.Client, error) {
	return scaleset.NewClientWithGitHubApp(scaleset.ClientWithGitHubAppConfig{
		GitHubConfigURL: configURL,
		GitHubAppAuth: scaleset.GitHubAppAuth{
			ClientID:       clientID,
			InstallationID: installationID,
			PrivateKey:     privateKey,
		},
		// Sent with every request (user agent), so GitHub can tell who is calling.
		SystemInfo: scaleset.SystemInfo{System: "kubevirt-scaleset", Version: version, Subsystem: "scaler"},
	})
}

// EnsureScaleSet returns the scale set called name in runnerGroup, creating it if needed.
//
// Unlike the library's Docker example, the scaler never deletes its scale set on exit:
// a restart (new image, node drain) must not drop queued jobs or change the scale set's ID.
// Its only label is its name, so workflows target it with `runs-on: <name>`.
func EnsureScaleSet(ctx context.Context, c *scaleset.Client, runnerGroup, name string, log *slog.Logger) (*scaleset.RunnerScaleSet, error) {
	groupID := 1 // the org's Default group always has ID 1
	if runnerGroup != scaleset.DefaultRunnerGroup {
		g, err := c.GetRunnerGroupByName(ctx, runnerGroup)
		if err != nil {
			return nil, fmt.Errorf("runner group %q: %w", runnerGroup, err)
		}
		groupID = g.ID
	}

	existing, err := c.GetRunnerScaleSet(ctx, groupID, name)
	if err != nil {
		return nil, fmt.Errorf("get scale set %q: %w", name, err)
	}
	if existing != nil {
		log.Info("using existing scale set", "id", existing.ID, "name", existing.Name, "runnerGroupID", groupID)
		return existing, nil
	}

	created, err := c.CreateRunnerScaleSet(ctx, &scaleset.RunnerScaleSet{
		Name:          name,
		RunnerGroupID: groupID,
		Labels:        []scaleset.Label{{Name: name}},
		// The runner version is baked into the VM image; a self-update at job start would
		// only cost time and then be thrown away with the VM.
		RunnerSetting: scaleset.RunnerSetting{DisableUpdate: true},
	})
	if err != nil {
		return nil, fmt.Errorf("create scale set %q: %w", name, err)
	}
	log.Info("created scale set", "id", created.ID, "name", created.Name, "runnerGroupID", groupID)
	return created, nil
}
