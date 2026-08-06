package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nkcx/canarium/internal/api"
	"github.com/nkcx/canarium/internal/conditions"
	"github.com/nkcx/canarium/internal/config"
	"github.com/nkcx/canarium/internal/engine"
	"github.com/nkcx/canarium/internal/facts"
	"github.com/nkcx/canarium/internal/notify"
	"github.com/nkcx/canarium/internal/state"
	"github.com/nkcx/canarium/internal/simulate"
	nutmod "github.com/nkcx/canarium/modules/nut"
	"github.com/nkcx/canarium/modules/proxmox"
	"github.com/nkcx/canarium/modules/opnsense"
	snmpmod "github.com/nkcx/canarium/modules/snmp"
	"github.com/nkcx/canarium/modules/ssh"
	"github.com/nkcx/canarium/modules/truenas"
	"github.com/nkcx/canarium/modules/wol"
	execmod "github.com/nkcx/canarium/modules/exec"
	restmod "github.com/nkcx/canarium/modules/rest"
	canarium "github.com/nkcx/canarium"
	"github.com/spf13/cobra"
)

var version = "dev"

func main() {
	root := &cobra.Command{
		Use:     "canarium",
		Short:   "Power-event orchestrator",
		Version: version,
	}

	var configPath string
	root.PersistentFlags().StringVarP(&configPath, "config", "c", "/etc/canarium/config.yaml", "config file path")

	root.AddCommand(
		runCmd(&configPath),
		validateCmd(&configPath),
		doctorCmd(&configPath),
		simulateCmd(&configPath),
	)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func runCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Start the Canarium daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDaemon(*configPath)
		},
	}
}

func validateCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Validate configuration (offline, deterministic)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(*configPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error loading config: %s\n", err)
				return err
			}

			result := config.Validate(cfg)

			for _, e := range result.Errors {
				fmt.Fprintf(os.Stderr, "ERROR: %s\n", e)
			}
			for _, w := range result.Warnings {
				fmt.Fprintf(os.Stderr, "WARN:  %s\n", w)
			}
			for _, i := range result.Info {
				fmt.Fprintf(os.Stdout, "INFO:  %s\n", i)
			}

			if result.HasErrors() {
				fmt.Fprintf(os.Stderr, "\nValidation failed with %d error(s)\n", len(result.Errors))
				return fmt.Errorf("validation failed")
			}

			fmt.Println("Configuration is valid.")
			return nil
		},
	}
}

func doctorCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Run live preflight checks (connectivity, credentials, capabilities)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(*configPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error loading config: %s\n", err)
				return err
			}

			result := config.Validate(cfg)
			if result.HasErrors() {
				for _, e := range result.Errors {
					fmt.Fprintf(os.Stderr, "ERROR: %s\n", e)
				}
				return fmt.Errorf("config validation failed, fix errors before running doctor")
			}

			fmt.Println("Config validation passed. Live checks not yet implemented.")
			return nil
		},
	}
}

func simulateCmd(configPath *string) *cobra.Command {
	var planName string
	var timelinePath string

	cmd := &cobra.Command{
		Use:   "simulate",
		Short: "Simulate a plan against a fact timeline",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(*configPath)
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			result := config.Validate(cfg)
			if result.HasErrors() {
				return fmt.Errorf("config validation failed")
			}

			tl, err := simulate.LoadTimeline(timelinePath)
			if err != nil {
				return fmt.Errorf("loading timeline: %w", err)
			}

			logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
				Level: slog.LevelInfo,
			}))

			simResult, err := simulate.Run(cfg, tl, planName, logger)
			if err != nil {
				return err
			}

			fmt.Printf("\nSimulation complete:\n")
			fmt.Printf("  Triggers:   %d\n", len(simResult.Triggers))
			fmt.Printf("  Stages:     %d\n", len(simResult.Stages))
			fmt.Printf("  Aborts:     %d\n", len(simResult.Aborts))
			fmt.Printf("  Wake gates: %d\n", len(simResult.WakeGates))

			return nil
		},
	}

	cmd.Flags().StringVar(&planName, "plan", "", "plan name to simulate")
	cmd.Flags().StringVar(&timelinePath, "timeline", "", "path to timeline JSON file")
	cmd.MarkFlagRequired("plan")
	cmd.MarkFlagRequired("timeline")

	return cmd
}

func runDaemon(configPath string) error {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	result := config.Validate(cfg)
	if result.HasErrors() {
		for _, e := range result.Errors {
			logger.Error("config error", "error", e)
		}
		return fmt.Errorf("config validation failed")
	}
	for _, w := range result.Warnings {
		logger.Warn("config warning", "warning", w)
	}

	db, err := state.Open(cfg.Canarium.DataDir)
	if err != nil {
		return fmt.Errorf("opening state database: %w", err)
	}
	defer db.Close()

	store := facts.NewStore()
	evaluator := conditions.NewEvaluator(store)
	executor := engine.NewExecutor(cfg, store, evaluator, db, logger)

	registerTransports(executor, cfg, logger)
	registerSources(store, cfg, logger, executor)

	if len(cfg.Canarium.Notifications.Webhooks) > 0 {
		notifier := notify.NewWebhookNotifier(cfg.Canarium.Notifications.Webhooks, logger)
		executor.AddListener(notifier.HandleEvent)
	}

	server := api.NewServer(cfg, store, executor, db, canarium.WebFS, logger)
	executor.AddListener(server.EventListener())

	if err := executor.Start(); err != nil {
		return fmt.Errorf("starting executor: %w", err)
	}

	go func() {
		if err := server.Start(cfg.Canarium.Host); err != nil {
			logger.Error("API server error", "error", err)
		}
	}()

	logger.Info("Canarium started",
		"version", version,
		"mode", cfg.Canarium.Mode,
		"clients", len(cfg.Clients),
		"plans", len(cfg.Plans),
		"address", cfg.Canarium.Host,
	)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	logger.Info("shutting down")
	executor.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	server.Stop(ctx)

	return nil
}

func registerTransports(executor *engine.Executor, cfg *config.Config, logger *slog.Logger) {
	executor.RegisterTransport("ssh", ssh.New(ssh.Config{}))
	executor.RegisterTransport("wol", wol.New(wol.Config{}))
	executor.RegisterTransport("exec", execmod.New())
	executor.RegisterTransport("rest", restmod.New())
	executor.RegisterTransport("proxmox", proxmox.New())
	executor.RegisterTransport("truenas", truenas.New(logger))
	executor.RegisterTransport("opnsense", opnsense.New())
	executor.RegisterTransport("nut", nutmod.NewTransport(logger))
	executor.RegisterTransport("snmp-poe", snmpmod.NewPoeTransport(logger))
}

func registerSources(store *facts.Store, cfg *config.Config, logger *slog.Logger, executor *engine.Executor) {
	for _, src := range cfg.Sources {
		switch src.Type {
		case "nut":
			var instances []nutmod.InstanceConfig
			if cfgData, ok := src.Config["instances"].([]any); ok {
				for _, inst := range cfgData {
					if m, ok := inst.(map[string]any); ok {
						ic := nutmod.InstanceConfig{
							Name: getStr(m, "name"),
							Host: getStr(m, "host"),
							UPS:  getStr(m, "ups"),
							PollInterval: getStr(m, "poll_interval"),
						}
						if p, ok := m["port"].(int); ok {
							ic.Port = p
						}
						instances = append(instances, ic)
					}
				}
			}

			nutCfg := nutmod.Config{Instances: instances}
			nutmod.RegisterFacts(store, nutCfg)

			source := nutmod.NewSource(nutCfg, logger)
			updates := make(chan engine.FactUpdate, 100)
			go func() {
				source.Start(context.Background(), updates)
			}()
			go func() {
				for update := range updates {
					store.Update(update.Key, update.Value, update.Timestamp)
				}
			}()

		case "snmp":
			var instances []snmpmod.InstanceConfig
			if cfgData, ok := src.Config["instances"].([]any); ok {
				for _, inst := range cfgData {
					if m, ok := inst.(map[string]any); ok {
						ic := snmpmod.InstanceConfig{
							Name:         getStr(m, "name"),
							Host:         getStr(m, "host"),
							PollInterval: getStr(m, "poll_interval"),
							Community:    getStr(m, "snmp_community"),
							User:         getStr(m, "snmp_user"),
							AuthPass:     getStr(m, "snmp_auth_pass"),
							PrivPass:     getStr(m, "snmp_priv_pass"),
						}
						if p, ok := m["port"].(int); ok {
							ic.Port = p
						}
						if v, ok := m["snmp_version"].(int); ok {
							ic.Version = v
						}
						if oidData, ok := m["oids"].([]any); ok {
							for _, oidItem := range oidData {
								if om, ok := oidItem.(map[string]any); ok {
									ic.OIDs = append(ic.OIDs, snmpmod.OIDSpec{
										OID:  getStr(om, "oid"),
										Name: getStr(om, "name"),
										Type: getStr(om, "type"),
									})
								}
							}
						}
						instances = append(instances, ic)
					}
				}
			}

			snmpCfg := snmpmod.Config{Instances: instances}
			snmpmod.RegisterFacts(store, snmpCfg)

			snmpSource := snmpmod.NewSource(snmpCfg, logger)
			snmpUpdates := make(chan engine.FactUpdate, 100)
			go func() {
				snmpSource.Start(context.Background(), snmpUpdates)
			}()
			go func() {
				for update := range snmpUpdates {
					store.Update(update.Key, update.Value, update.Timestamp)
				}
			}()
		}
	}
}

func getStr(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}
