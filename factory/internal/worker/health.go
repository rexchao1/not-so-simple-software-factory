package worker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

const healthCheckTimeout = 10 * time.Second

type health struct {
	State          string
	GitVersion     string
	RuntimeVersion string
	Capabilities   []protocol.Capability
	SourceAccess   []protocol.SourceAccess
	WeeklyLimit    *protocol.WeeklyLimit
	Error          error
}

func checkHealth(
	ctx context.Context,
	gitExecutable string,
	githubExecutable string,
	primaryRuntime string,
	runtimes []string,
	runtimeExecutables map[string]string,
) health {
	result := health{State: "unhealthy"}
	var gitCapability protocol.Capability
	var githubCapability protocol.Capability
	runtimeCapabilities := make([]protocol.Capability, len(runtimes))
	var weeklyLimit *protocol.WeeklyLimit
	probes := []func(){
		func() {
			gitCapability = versionCapability(ctx, protocol.CapabilityKindTool, "git", gitExecutable)
		},
		func() {
			githubCapability = githubHealthCapability(ctx, githubExecutable)
		},
	}
	for index, runtime := range runtimes {
		index, runtime := index, runtime
		probes = append(probes, func() {
			runtimeCapabilities[index] = runtimeCapability(ctx, runtime, runtimeExecutables[runtime])
		})
	}
	if configuredRuntime(runtimes, protocol.RuntimeCodex) {
		probes = append(probes, func() {
			limitContext, limitCancel := context.WithTimeout(ctx, healthCheckTimeout)
			defer limitCancel()
			weeklyLimit, _ = readCodexWeeklyLimit(limitContext, runtimeExecutables[protocol.RuntimeCodex])
		})
	}
	runHealthProbes(probes...)

	result.Capabilities = append(result.Capabilities, gitCapability)
	if gitCapability.Status == protocol.CapabilityReady {
		result.GitVersion = gitCapability.Version
	}

	result.Capabilities = append(result.Capabilities, githubCapability)
	if githubCapability.Status == protocol.CapabilityReady {
		result.SourceAccess = append(result.SourceAccess, protocol.SourceAccess{
			Provider: "github", Hostname: "github.com",
		})
	}

	readyRuntime := false
	for index, runtime := range runtimes {
		capability := runtimeCapabilities[index]
		result.Capabilities = append(result.Capabilities, capability)
		if runtime == primaryRuntime {
			result.RuntimeVersion = capability.Version
		}
		if capability.Status == protocol.CapabilityReady {
			readyRuntime = true
		}
	}
	if capabilityReady(result.Capabilities, protocol.CapabilityKindRuntime, protocol.RuntimeCodex) {
		result.WeeklyLimit = weeklyLimit
	}
	if gitCapability.Status == protocol.CapabilityReady && readyRuntime {
		result.State = "healthy"
		return result
	}
	if gitCapability.Status != protocol.CapabilityReady {
		result.Error = errors.New(gitCapability.Message)
	} else {
		result.Error = errors.New("no configured coding agent runtime is ready")
	}
	return result
}

func runHealthProbes(probes ...func()) {
	var waitGroup sync.WaitGroup
	waitGroup.Add(len(probes))
	for _, probe := range probes {
		go func() {
			defer waitGroup.Done()
			probe()
		}()
	}
	waitGroup.Wait()
}

func configuredRuntime(runtimes []string, target string) bool {
	for _, runtime := range runtimes {
		if runtime == target {
			return true
		}
	}
	return false
}

func githubHealthCapability(ctx context.Context, executable string) protocol.Capability {
	probeContext, probeCancel := context.WithTimeout(ctx, healthCheckTimeout)
	defer probeCancel()
	capability := versionCapability(probeContext, protocol.CapabilityKindTool, "gh", executable)
	if capability.Status != protocol.CapabilityReady {
		return capability
	}
	githubContext, githubCancel := context.WithTimeout(probeContext, healthCheckTimeout)
	defer githubCancel()
	_, _, err := runCommand(
		githubContext, executable, "", 64<<10,
		"auth", "status", "--hostname", "github.com",
	)
	if err != nil {
		capability.Status = protocol.CapabilityUnauthenticated
		capability.Message = "Run gh auth login --hostname github.com."
	}
	return capability
}

func versionCapability(
	ctx context.Context,
	kind string,
	name string,
	executable string,
) protocol.Capability {
	capability := protocol.Capability{Kind: kind, Name: name, Status: protocol.CapabilityUnhealthy}
	if strings.TrimSpace(executable) == "" {
		capability.Status = protocol.CapabilityMissing
		capability.Message = fmt.Sprintf("Configure the %s executable.", name)
		return capability
	}
	commandContext, cancel := context.WithTimeout(ctx, healthCheckTimeout)
	stdout, _, err := runCommand(commandContext, executable, "", 64<<10, "--version")
	cancel()
	if err != nil {
		var executableError *exec.Error
		if errors.As(err, &executableError) || errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			capability.Status = protocol.CapabilityMissing
			capability.Message = fmt.Sprintf("Install %s and make it available on PATH.", name)
			return capability
		}
		capability.Message = fmt.Sprintf("%s version check failed.", name)
		return capability
	}
	capability.Version = strings.TrimSpace(string(stdout))
	if capability.Version == "" {
		capability.Message = fmt.Sprintf("%s version check returned no version.", name)
		return capability
	}
	capability.Status = protocol.CapabilityReady
	return capability
}

func runtimeCapability(ctx context.Context, runtime, executable string) protocol.Capability {
	return runtimeCapabilityWithin(ctx, runtime, executable, healthCheckTimeout)
}

func runtimeCapabilityWithin(
	ctx context.Context,
	runtime string,
	executable string,
	timeout time.Duration,
) protocol.Capability {
	probeContext, probeCancel := context.WithTimeout(ctx, timeout)
	defer probeCancel()
	capability := versionCapability(probeContext, protocol.CapabilityKindRuntime, runtime, executable)
	if capability.Status != protocol.CapabilityReady {
		return capability
	}
	commandContext, cancel := context.WithTimeout(probeContext, timeout)
	defer cancel()
	var stdout, stderr []byte
	var err error
	switch runtime {
	case protocol.RuntimePi:
		stdout, stderr, err = runCommand(commandContext, executable, "", 64<<10, "--list-models")
	case protocol.RuntimeClaudeCode:
		stdout, stderr, err = runCommand(commandContext, executable, "", 64<<10, "auth", "status", "--json")
	default:
		stdout, stderr, err = runCommand(commandContext, executable, "", 64<<10, "login", "status")
	}
	if err != nil {
		capability.Status = protocol.CapabilityUnauthenticated
		capability.Message = fmt.Sprintf("Authenticate %s before running Sessions.", runtimeDisplayName(runtime))
		return capability
	}
	switch runtime {
	case protocol.RuntimePi:
		lines := strings.Split(strings.TrimSpace(string(stdout)), "\n")
		if len(lines) < 2 {
			capability.Status = protocol.CapabilityUnauthenticated
			capability.Message = "Configure a Pi provider or subscription before running Sessions."
		}
	case protocol.RuntimeClaudeCode:
		var status struct {
			LoggedIn bool `json:"loggedIn"`
		}
		if json.Unmarshal(stdout, &status) != nil || !status.LoggedIn {
			capability.Status = protocol.CapabilityUnauthenticated
			capability.Message = "Authenticate Claude Code before running Sessions."
		}
	default:
		if strings.TrimSpace(string(stdout))+strings.TrimSpace(string(stderr)) == "" {
			capability.Status = protocol.CapabilityUnauthenticated
			capability.Message = "Authenticate Codex before running Sessions."
		}
	}
	return capability
}

func capabilityReady(capabilities []protocol.Capability, kind, name string) bool {
	for _, capability := range capabilities {
		if capability.Kind == kind && capability.Name == name && capability.Status == protocol.CapabilityReady {
			return true
		}
	}
	return false
}

type codexRateLimitWindow struct {
	UsedPercent        int    `json:"usedPercent"`
	WindowDurationMins *int64 `json:"windowDurationMins"`
	ResetsAt           *int64 `json:"resetsAt"`
}

type codexRateLimitSnapshot struct {
	Primary   *codexRateLimitWindow `json:"primary"`
	Secondary *codexRateLimitWindow `json:"secondary"`
}

func readCodexWeeklyLimit(ctx context.Context, executable string) (*protocol.WeeklyLimit, error) {
	command := exec.CommandContext(ctx, executable, "app-server", "--listen", "stdio://")
	configureNewProcessGroup(command)
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	command.Stderr = newLimitBuffer(64 << 10)
	if err := command.Start(); err != nil {
		return nil, err
	}
	pid := command.Process.Pid
	defer func() {
		_ = stdin.Close()
		done := make(chan struct{})
		go func() {
			_ = command.Wait()
			close(done)
		}()
		select {
		case <-done:
			return
		case <-time.After(time.Second):
		}
		if identity, err := processIdentity(pid); err == nil {
			_ = stopOwnedProcessGroup(pid, identity, time.Second)
		} else {
			_ = forceStopStartedProcessGroup(pid)
		}
		<-done
	}()

	encoder := json.NewEncoder(stdin)
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), protocol.MaxBodyBytes)
	if err := encoder.Encode(map[string]any{
		"method": "initialize", "id": 1,
		"params": map[string]any{"clientInfo": map[string]string{
			"name": "factory", "title": "Factory", "version": "1",
		}},
	}); err != nil {
		return nil, err
	}
	if err := readCodexResponse(scanner, 1, nil); err != nil {
		return nil, err
	}
	if err := encoder.Encode(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		return nil, err
	}
	if err := encoder.Encode(map[string]any{
		"method": "account/rateLimits/read", "id": 2, "params": map[string]any{},
	}); err != nil {
		return nil, err
	}
	var result struct {
		RateLimits          codexRateLimitSnapshot            `json:"rateLimits"`
		RateLimitsByLimitID map[string]codexRateLimitSnapshot `json:"rateLimitsByLimitId"`
	}
	if err := readCodexResponse(scanner, 2, &result); err != nil {
		return nil, err
	}
	snapshot, ok := result.RateLimitsByLimitID["codex"]
	if !ok {
		snapshot = result.RateLimits
	}
	for _, window := range []*codexRateLimitWindow{snapshot.Primary, snapshot.Secondary} {
		if window != nil && window.WindowDurationMins != nil &&
			*window.WindowDurationMins == int64((7*24*time.Hour)/time.Minute) &&
			window.ResetsAt != nil && *window.ResetsAt > 0 &&
			window.UsedPercent >= 0 && window.UsedPercent <= 100 {
			return &protocol.WeeklyLimit{
				UsedPercent: window.UsedPercent,
				ResetsAt:    time.Unix(*window.ResetsAt, 0).UTC(),
			}, nil
		}
	}
	return nil, errors.New("Codex did not return a weekly rate-limit window")
}

func readCodexResponse(scanner *bufio.Scanner, id int, target any) error {
	for scanner.Scan() {
		var message struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			return err
		}
		if message.ID != id {
			continue
		}
		if message.Error != nil {
			return errors.New(message.Error.Message)
		}
		if target == nil {
			return nil
		}
		return json.Unmarshal(message.Result, target)
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return errors.New("Codex app server closed before replying")
}

func runtimeDisplayName(runtime string) string {
	switch runtime {
	case protocol.RuntimePi:
		return "Pi"
	case protocol.RuntimeClaudeCode:
		return "Claude Code"
	default:
		return "Codex"
	}
}
