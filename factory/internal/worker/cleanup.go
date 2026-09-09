package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

type CleanupOptions struct {
	AttemptID     string
	Confirm       bool
	GitExecutable string
}

type cleanupPreview struct {
	Manifest           attemptManifest `json:"manifest"`
	RepositoryIdentity string          `json:"repository_identity"`
	Branch             string          `json:"branch"`
	Commit             string          `json:"commit,omitempty"`
	GitStatus          string          `json:"git_status"`
	Reason             string          `json:"reason"`
	PathExists         bool            `json:"path_exists"`
	GitRegistered      bool            `json:"git_registered"`
}

func Cleanup(config Config, options CleanupOptions, output io.Writer) error {
	if err := ensureSupportedPlatform(); err != nil {
		return err
	}
	if err := validateConfig(config); err != nil {
		return err
	}
	if !uuidPattern.MatchString(options.AttemptID) {
		return errors.New("cleanup requires a valid attempt ID")
	}
	if options.GitExecutable == "" {
		options.GitExecutable = "git"
	}
	if output == nil {
		output = os.Stdout
	}
	dataDirectory, err := resolveDataDirectory(config.DataDirectory)
	if err != nil {
		return err
	}
	lock, err := lockDataDirectory(dataDirectory)
	if err != nil {
		return err
	}
	defer lock.Close()
	workerID, err := loadWorkerID(dataDirectory)
	if err != nil {
		return err
	}
	store := newManifestStore(dataDirectory, workerID)
	manifest, err := store.load(options.AttemptID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*gitCommandTimeout)
	defer cancel()
	inspection, inspectErr := inspectManifestWorktree(ctx, options.GitExecutable, dataDirectory, manifest)
	preview := cleanupPreview{
		Manifest: manifest, RepositoryIdentity: manifest.RemoteIdentity, Branch: manifest.Branch,
		Reason: manifest.RetentionReason, PathExists: inspection.PathExists,
		GitRegistered: inspection.Registered,
	}
	if inspection.Registered {
		preview.Commit = inspection.Entry.Head
	}
	if inspectErr != nil || !inspection.PathExists || !inspection.Registered {
		preview.GitStatus = "unavailable"
	} else if inspection.Status == "" {
		preview.GitStatus = "clean"
	} else {
		preview.GitStatus = inspection.Status
	}
	if err := writeCleanupPreview(output, preview); err != nil {
		return err
	}
	if inspectErr != nil {
		return inspectErr
	}
	if !options.Confirm {
		return nil
	}
	if manifest.ProcessActive {
		return errors.New("refuse cleanup while the manifest has unreconciled process identity")
	}
	if manifest.Lifecycle != manifestRetained && manifest.Lifecycle != manifestCleanupStarted {
		return fmt.Errorf("attempt lifecycle %q is not eligible for cleanup", manifest.Lifecycle)
	}
	if !inspection.PathExists && !inspection.Registered && manifest.Lifecycle == manifestCleanupStarted {
		_, err := store.update(manifest.AttemptID, func(value *attemptManifest) error {
			value.Lifecycle = manifestCleaned
			value.CleanupResult = "confirmed cleanup found the worktree already absent"
			value.RetentionReason = ""
			return nil
		})
		return err
	}
	if inspection.PathExists != inspection.Registered {
		return errors.New("refuse cleanup because filesystem and Git worktree identity disagree")
	}
	if !inspection.PathExists {
		return errors.New("refuse cleanup because the retained worktree is absent")
	}
	if _, err := store.update(manifest.AttemptID, func(value *attemptManifest) error {
		value.Lifecycle = manifestCleanupStarted
		value.CleanupIntent = cleanupIntentOperator
		value.CleanupResult = "operator confirmed cleanup"
		return nil
	}); err != nil {
		return err
	}
	manifest, err = store.load(options.AttemptID)
	if err != nil {
		return err
	}
	inspection, err = inspectManifestWorktree(ctx, options.GitExecutable, dataDirectory, manifest)
	if err != nil {
		return err
	}
	if err := removeInspectedWorktree(ctx, options.GitExecutable, inspection, true); err != nil {
		return err
	}
	_, err = store.update(manifest.AttemptID, func(value *attemptManifest) error {
		value.Lifecycle = manifestCleaned
		value.CleanupResult = "operator cleanup completed"
		value.RetentionReason = ""
		return nil
	})
	if err == nil {
		_, _ = fmt.Fprintf(output, "cleanup confirmed for attempt %s; branch %s was preserved\n",
			manifest.AttemptID, manifest.Branch)
	}
	return err
}

func writeCleanupPreview(output io.Writer, preview cleanupPreview) error {
	body, err := json.MarshalIndent(preview, "", "  ")
	if err != nil {
		return fmt.Errorf("encode cleanup preview: %w", err)
	}
	if _, err := output.Write(append(body, '\n')); err != nil {
		return fmt.Errorf("write cleanup preview: %w", err)
	}
	return nil
}

func ParseCleanupArguments(arguments []string, defaultConfig string) (string, CleanupOptions, error) {
	configPath := defaultConfig
	options := CleanupOptions{}
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		switch {
		case argument == "--confirm":
			options.Confirm = true
		case argument == "--config":
			index++
			if index >= len(arguments) {
				return "", CleanupOptions{}, errors.New("--config requires a path")
			}
			configPath = arguments[index]
		case strings.HasPrefix(argument, "--config="):
			configPath = strings.TrimPrefix(argument, "--config=")
			if configPath == "" {
				return "", CleanupOptions{}, errors.New("--config requires a path")
			}
		case strings.HasPrefix(argument, "-"):
			return "", CleanupOptions{}, fmt.Errorf("unknown cleanup option %q", argument)
		case options.AttemptID == "":
			options.AttemptID = argument
		default:
			return "", CleanupOptions{}, fmt.Errorf("unexpected cleanup argument %q", argument)
		}
	}
	if options.AttemptID == "" {
		return "", CleanupOptions{}, errors.New("usage: factory-worker cleanup ATTEMPT_ID [--confirm] [--config PATH]")
	}
	return configPath, options, nil
}
