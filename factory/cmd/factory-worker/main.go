package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"

	"github.com/owainlewis/factory/internal/buildinfo"
	"github.com/owainlewis/factory/internal/worker"
)

func main() {
	if buildinfo.Requested(os.Args[1:]) {
		fmt.Fprintln(os.Stdout, buildinfo.String("factory-worker"))
		return
	}
	if worker.IsSupervisorCommand(os.Args[1:]) {
		control := os.NewFile(3, "factory-worker-control")
		if err := worker.RunSupervisor(control, os.Stdin, os.Stdout, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, "factory-worker supervisor:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "factory-worker:", err)
		os.Exit(1)
	}
}

func run() error {
	defaultConfig, err := worker.DefaultConfigPath()
	if err != nil {
		return err
	}
	if len(os.Args) > 1 && os.Args[1] == "cleanup" {
		arguments := os.Args[2:]
		configPath, options, err := worker.ParseCleanupArguments(arguments, defaultConfig)
		if err != nil {
			return err
		}
		if err := validateDefaultConfigSelection(cleanupConfigExplicit(arguments)); err != nil {
			return err
		}
		config, err := worker.LoadConfig(configPath)
		if err != nil {
			return err
		}
		return worker.Cleanup(config, options, os.Stdout)
	}
	if len(os.Args) > 1 && os.Args[1] == "identity" {
		flags := flag.NewFlagSet("factory-worker identity", flag.ContinueOnError)
		configPath := flags.String("config", defaultConfig, "Factory worker TOML configuration path")
		if err := flags.Parse(os.Args[2:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return fmt.Errorf("unexpected identity arguments: %v", flags.Args())
		}
		if err := validateDefaultConfigSelection(flagExplicit(flags, "config")); err != nil {
			return err
		}
		config, err := worker.LoadConfig(*configPath)
		if err != nil {
			return err
		}
		id, err := worker.ResolveWorkerID(config)
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, id)
		return nil
	}
	flags := flag.NewFlagSet("factory-worker", flag.ContinueOnError)
	configPath := flags.String("config", defaultConfig, "Factory worker TOML configuration path")
	if err := flags.Parse(os.Args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	if err := validateDefaultConfigSelection(flagExplicit(flags, "config")); err != nil {
		return err
	}
	config, err := worker.LoadConfig(*configPath)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	manager, err := worker.New(config, worker.Options{WorkerVersion: buildinfo.Version}, logger)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), worker.ShutdownSignals()...)
	defer stop()
	logger.Info("worker_started",
		"worker_id", manager.ID(),
		"name", config.Name,
		"runtimes", config.Runtimes,
		"server", config.Server,
		"data_directory", config.DataDirectory,
		"repository_count", len(config.Repositories),
	)
	if err := manager.Run(ctx); err != nil {
		return err
	}
	logger.Info("worker_stopped", "worker_id", manager.ID())
	return nil
}

func flagExplicit(flags *flag.FlagSet, name string) bool {
	found := false
	flags.Visit(func(value *flag.Flag) {
		if value.Name == name {
			found = true
		}
	})
	return found
}

func cleanupConfigExplicit(arguments []string) bool {
	for _, argument := range arguments {
		if argument == "--config" || strings.HasPrefix(argument, "--config=") {
			return true
		}
	}
	return false
}

func validateDefaultConfigSelection(configExplicit bool) error {
	if configExplicit {
		return nil
	}
	return worker.ValidateNoLegacyDefaultConfig()
}
