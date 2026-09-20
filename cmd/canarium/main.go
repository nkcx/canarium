package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	canarium "github.com/nkcx/canarium"
	"github.com/nkcx/canarium/internal/api"
	"github.com/nkcx/canarium/internal/conditions"
	"github.com/nkcx/canarium/internal/config"
	"github.com/nkcx/canarium/internal/doctor"
	"github.com/nkcx/canarium/internal/engine"
	"github.com/nkcx/canarium/internal/facts"
	"github.com/nkcx/canarium/internal/notify"
	"github.com/nkcx/canarium/internal/simulate"
	"github.com/nkcx/canarium/internal/state"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var version = "dev"

// shutdownGrace is how long in-flight HTTP requests have to finish.
const shutdownGrace = 10 * time.Second

// newLogger builds the daemon's structured logger.
func newLogger(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}))
}

func main() {
	// The config package cannot import conditions (which imports config), so
	// expression type-checking is injected here. Without this, ValidateExpr
	// was never called and a malformed template was not a config error — it
	// simply evaluated to unavailable forever and its condition never fired.
	config.ExprValidator = conditions.ValidateExpr

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
		tokenCmd(&configPath),
		hashPasswordCmd(),
	)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

// loadAndValidate loads a config and checks it against what this build
// actually supports.
//
// Building a registry from the live transport and source tables means
// validation cannot drift from the daemon: adding a transport makes it valid
// in configs automatically, and removing one turns existing configs into
// errors rather than silent runtime skips.
func loadAndValidate(configPath string, logger *slog.Logger, opts config.LoadOptions) (*config.Config, *config.ValidationResult, error) {
	loaded, err := config.LoadWith(configPath, opts)
	if err != nil {
		return nil, nil, err
	}
	cfg := loaded.Config

	// Fact declarations come from the configured sources, so the store must
	// be populated before fact references can be checked.
	store := facts.NewStore()
	if _, err := NewSourceManager(cfg, store, logger); err != nil {
		// Report this as a validation error rather than a hard failure, so
		// the operator sees every problem at once.
		result := &config.ValidationResult{}
		result.AddError("%s", err)
		return cfg, result, nil
	}

	registry := buildRegistry(cfg, store, logger)
	result := config.ValidateWith(cfg, registry)

	for _, name := range loaded.MissingEnv {
		result.AddWarning("environment variable %s is not set; "+
			"a placeholder was substituted for validation. "+
			"The daemon will refuse to start until it is set.", name)
	}

	return cfg, result, nil
}

// reportValidation prints a validation result and reports whether it failed.
func reportValidation(result *config.ValidationResult, verbose bool) bool {
	for _, e := range result.Errors {
		fmt.Fprintf(os.Stderr, "ERROR: %s\n", e)
	}
	for _, w := range result.Warnings {
		fmt.Fprintf(os.Stderr, "WARN:  %s\n", w)
	}
	if verbose {
		for _, i := range result.Info {
			fmt.Fprintf(os.Stdout, "INFO:  %s\n", i)
		}
	}
	return result.HasErrors()
}

// hashPasswordCmd produces a hash suitable for canarium.auth.password_hash.
func hashPasswordCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "hash-password",
		Short: "Hash a password for canarium.auth.password_hash",
		Long: "Read a password from the terminal and print its bcrypt hash.\n\n" +
			"Put the result in canarium.auth.password_hash to pin the admin\n" +
			"password in the configuration file rather than the database. Useful\n" +
			"for immutable deployments whose database is ephemeral.\n\n" +
			"The password is read from the terminal without echoing, so it does\n" +
			"not end up in shell history.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			interactive := term.IsTerminal(int(os.Stdin.Fd()))

			password, err := readPassword("Password: ", interactive)
			if err != nil {
				return err
			}
			if len(password) < api.MinPasswordLength {
				return fmt.Errorf("password must be at least %d characters", api.MinPasswordLength)
			}

			// Confirmation guards against a typo nobody can see. Piped input
			// has already been typed once somewhere else, and a second read
			// would consume a line the caller did not intend to supply.
			if interactive {
				confirm, err := readPassword("Confirm: ", true)
				if err != nil {
					return err
				}
				if password != confirm {
					return fmt.Errorf("passwords do not match")
				}
			}

			hash, err := api.HashPassword(password)
			if err != nil {
				return err
			}

			fmt.Printf("\ncanarium:\n  auth:\n    password_hash: %q\n", hash)
			return nil
		},
	}
}

// readPassword reads a password, without echoing when attached to a
// terminal.
//
// Prompts go to stderr so `canarium hash-password > config-snippet.yaml`
// works without the prompt ending up in the file.
func readPassword(prompt string, interactive bool) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	defer fmt.Fprintln(os.Stderr)

	if !interactive {
		// Piped input, for scripted use. Read exactly one line: buffering
		// ahead would swallow input the caller intended for something else.
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("reading password: %w", err)
		}
		line = strings.TrimSpace(line)
		if line == "" {
			return "", fmt.Errorf("no password supplied on stdin")
		}
		return line, nil
	}

	raw, err := term.ReadPassword(int(os.Stdin.Fd()))
	if err != nil {
		return "", fmt.Errorf("reading password: %w", err)
	}
	return strings.TrimSpace(string(raw)), nil
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
	var quiet bool
	var strictEnv bool

	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate configuration (offline, deterministic)",
		Long: "Check a configuration file for errors without contacting anything.\n\n" +
			"Verifies that transports and source types exist in this build, that every\n" +
			"client and tag reference resolves, that fact references match what the\n" +
			"configured sources declare, that template expressions type-check, that\n" +
			"durations parse, and that stage ordering is consistent with declared\n" +
			"dependencies. Exits non-zero on any error, so it is suitable for CI.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := newLogger(os.Stderr, slog.LevelWarn)

			// Unset environment variables are a warning here, not an
			// error: validating a config's structure in CI should not
			// require production secrets. --strict-env opts into the
			// daemon's behaviour.
			_, result, err := loadAndValidate(*configPath, logger, config.LoadOptions{
				AllowMissingEnv: !strictEnv,
			})
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			if reportValidation(result, !quiet) {
				fmt.Fprintf(os.Stderr, "\nValidation failed with %d error(s)\n", len(result.Errors))
				return fmt.Errorf("validation failed")
			}

			if len(result.Warnings) > 0 {
				fmt.Printf("Configuration is valid (%d warning(s)).\n", len(result.Warnings))
			} else {
				fmt.Println("Configuration is valid.")
			}
			return nil
		},
	}

	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "suppress informational output")
	cmd.Flags().BoolVar(&strictEnv, "strict-env", false,
		"fail if any referenced environment variable is unset, as the daemon does")
	return cmd
}

func doctorCmd(configPath *string) *cobra.Command {
	var timeout time.Duration
	var settle time.Duration

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Run live preflight checks (connectivity, credentials, capabilities)",
		Long: "Contact everything the config names and report whether it is usable.\n\n" +
			"Where `validate` checks a config against itself, doctor checks it against\n" +
			"reality: that sources authenticate and produce facts, that client\n" +
			"addresses resolve and answer, that credentials are present, and that the\n" +
			"state directory is writable.\n\n" +
			"Run this after changing anything, so a broken credential surfaces on a\n" +
			"Tuesday afternoon rather than during an outage.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := newLogger(os.Stderr, slog.LevelWarn)

			cfg, result, err := loadAndValidate(*configPath, logger, config.LoadOptions{})
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}
			if result.HasErrors() {
				reportValidation(result, false)
				return fmt.Errorf("config validation failed; fix these before running doctor")
			}

			store := facts.NewStore()
			sources, err := buildSources(cfg, store, logger)
			if err != nil {
				return fmt.Errorf("building sources: %w", err)
			}

			d := doctor.New(cfg, buildTransports(cfg, logger), sources, store, doctor.Options{
				Timeout:      timeout,
				SourceSettle: settle,
			})

			fmt.Printf("Running preflight checks (this contacts every configured host)...\n\n")
			report := d.Run(cmd.Context())

			printDoctorReport(report)

			if report.HasFailures() {
				return fmt.Errorf("preflight checks failed")
			}
			return nil
		},
	}

	cmd.Flags().DurationVar(&timeout, "timeout", doctor.DefaultOptions().Timeout,
		"per-check timeout")
	cmd.Flags().DurationVar(&settle, "source-settle", doctor.DefaultOptions().SourceSettle,
		"how long to wait for a source to produce its first facts")

	return cmd
}

// printDoctorReport renders a preflight report as an aligned table.
func printDoctorReport(report *doctor.Report) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "STATUS\tSUBJECT\tCHECK\tDETAIL")

	for _, c := range report.Checks {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", c.Status, c.Subject, c.Name, c.Detail)
	}
	w.Flush()

	counts := report.Counts()
	fmt.Printf("\n%d ok, %d warning(s), %d failure(s), %d skipped\n",
		counts[doctor.StatusOK], counts[doctor.StatusWarn],
		counts[doctor.StatusFail], counts[doctor.StatusSkip])
}

func simulateCmd(configPath *string) *cobra.Command {
	var planName string
	var timelinePath string
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "simulate",
		Short: "Simulate a plan against a fact timeline",
		Long: "Replay a scripted sequence of fact changes against a plan's policy.\n\n" +
			"Reports when the plan would trigger, when each stage's entry condition\n" +
			"would be satisfied, when abort would win, when the point of no return\n" +
			"would be crossed, and when the wake gate would open.\n\n" +
			"This simulates policy, not execution: it says nothing about whether a\n" +
			"transport authenticates or how long a host takes to shut down. Use\n" +
			"`canarium doctor` for that.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := newLogger(os.Stderr, slog.LevelWarn)

			cfg, result, err := loadAndValidate(*configPath, logger, config.LoadOptions{
				AllowMissingEnv: true,
			})
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}
			if result.HasErrors() {
				reportValidation(result, false)
				return fmt.Errorf("config validation failed")
			}

			timeline, err := simulate.LoadTimeline(timelinePath)
			if err != nil {
				return fmt.Errorf("loading timeline: %w", err)
			}

			// Simulation output is the point of the command, so it goes to
			// stdout at info level regardless of the daemon's log level.
			simLogger := newLogger(os.Stdout, slog.LevelInfo)
			if asJSON {
				simLogger = newLogger(io.Discard, slog.LevelError)
			}

			simResult, err := simulate.Run(cfg, timeline, planName, simLogger)
			if err != nil {
				return err
			}

			if asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(simResult)
			}

			printSimulationSummary(simResult, timeline)
			return nil
		},
	}

	cmd.Flags().StringVar(&planName, "plan", "", "plan name to simulate")
	cmd.Flags().StringVar(&timelinePath, "timeline", "", "path to timeline JSON file")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the result as JSON")

	if err := cmd.MarkFlagRequired("plan"); err != nil {
		panic(err) // only fails for a flag that does not exist
	}
	if err := cmd.MarkFlagRequired("timeline"); err != nil {
		panic(err)
	}

	return cmd
}

// printSimulationSummary renders a human-readable result.
func printSimulationSummary(r *simulate.SimulationResult, tl *simulate.Timeline) {
	fmt.Printf("\nSimulation of plan %q over %s:\n\n", r.Plan, tl.Duration.Duration())

	fmt.Printf("  Triggered:   %s\n", describeEvents(r.Triggers))
	fmt.Printf("  Stages run:  %s\n", describeEvents(r.Stages))
	fmt.Printf("  PONR:        %s\n", describeEvents(r.PONR))
	fmt.Printf("  Aborted:     %s\n", describeEvents(r.Aborts))
	fmt.Printf("  Wake gate:   %s\n", describeEvents(r.WakeGates))

	if len(r.ShutdownOrder) > 0 {
		fmt.Printf("\n  Would shut down, in order: %s\n", strings.Join(r.ShutdownOrder, ", "))
	}

	if len(r.NotShutDown) > 0 {
		fmt.Printf("\n  WOULD NOT BE SHUT DOWN: %s\n", strings.Join(r.NotShutDown, ", "))
		for _, e := range r.Skipped {
			fmt.Printf("    stage %q skipped at %s: %s\n", e.Stage, e.At, e.Detail)
		}
	}

	if !r.Completed {
		fmt.Printf("\n  The timeline ended before the wake gate opened. " +
			"Either it is too short, or the gate's conditions were never met.\n")
	}

	fmt.Println()
}

func describeEvents(events []simulate.Event) string {
	if len(events) == 0 {
		return "never"
	}

	parts := make([]string, 0, len(events))
	for _, e := range events {
		label := e.Stage
		if label == "" {
			label = e.Detail
		}
		parts = append(parts, fmt.Sprintf("%s at %s", label, e.At))
	}
	return strings.Join(parts, "; ")
}

func runDaemon(configPath string) error {
	logger := newLogger(os.Stderr, slog.LevelInfo)

	cfg, result, err := loadAndValidate(configPath, logger, config.LoadOptions{})
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	if result.HasErrors() {
		for _, e := range result.Errors {
			logger.Error("config error", "error", e)
		}
		return fmt.Errorf("config validation failed with %d error(s)", len(result.Errors))
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

	// Restore dwell progress so a restart partway through a "for: 5m"
	// condition does not silently start the five minutes again.
	if err := evaluator.SetDwellStore(db); err != nil {
		logger.Error("restoring dwell state; timers will start from zero", "error", err)
	}
	evaluator.SetPersistErrorHandler(func(key string, err error) {
		logger.Error("persisting dwell state", "condition", key, "error", err)
	})

	executor := engine.NewExecutor(cfg, store, evaluator, db, logger)

	registerTransports(executor, cfg, logger)

	sources, err := NewSourceManager(cfg, store, logger)
	if err != nil {
		return fmt.Errorf("configuring sources: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := sources.Start(ctx, store); err != nil {
		return fmt.Errorf("starting sources: %w", err)
	}
	defer sources.Stop()

	if len(cfg.Canarium.Notifications.Webhooks) > 0 {
		notifier := notify.NewWebhookNotifier(cfg.Canarium.Notifications.Webhooks, logger)
		executor.AddListener(notifier.HandleEvent)
		// Drain queued notifications before exiting, so the last events of a
		// sequence are not lost on shutdown.
		defer notifier.Close()
	}

	server := api.NewServer(cfg, store, executor, db, canarium.WebFS, logger)
	server.SetVersion(version)
	executor.AddListener(server.EventListener())

	warnIfNoAdminPassword(cfg, db, logger)

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

	<-ctx.Done()
	stop() // restore default signal handling, so a second signal kills us

	logger.Info("shutting down")
	executor.Stop()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := server.Stop(shutdownCtx); err != nil {
		logger.Error("shutting down the API server", "error", err)
	}

	return nil
}

// warnIfNoAdminPassword logs a prominent warning when no admin password has
// been set. Until one is, every authenticated endpoint refuses requests and
// the UI shows its first-run screen, so the daemon is not exposed — but the
// operator needs to know the web UI is not yet usable.
func warnIfNoAdminPassword(cfg *config.Config, db *state.DB, logger *slog.Logger) {
	if cfg.Canarium.Auth.PasswordHash != "" {
		logger.Info("admin password is pinned by the configuration file; " +
			"first-run setup is disabled")
		return
	}

	hash, err := db.GetPasswordHash()
	if err != nil {
		logger.Error("could not determine whether an admin password is set", "error", err)
		return
	}
	if hash == "" {
		logger.Warn("no admin password is set; the API will reject all requests " +
			"until one is created through the web UI's first-run screen")
	}
}
