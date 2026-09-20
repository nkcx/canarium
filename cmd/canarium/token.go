package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/nkcx/canarium/internal/config"
	"github.com/nkcx/canarium/internal/state"
	"github.com/spf13/cobra"
)

// apiTokenBytes is the size of a raw API token before hex encoding.
const apiTokenBytes = 32

// tokenCmd groups API token management.
//
// The api_tokens table and the code path that checks it have existed since
// the first commit, but nothing could ever create a row: there was no
// command, no endpoint, and no caller of SaveAPIToken. The Authorization
// header branch in requireAuth was unreachable.
func tokenCmd(configPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Manage API tokens",
		Long: "Create, list and revoke API tokens for non-interactive access.\n\n" +
			"Tokens are an alternative to the session cookie, for monitoring\n" +
			"systems and automation. A read-scoped token can observe the daemon\n" +
			"but cannot arm it, abort a sequence, or force a stage.",
	}

	cmd.AddCommand(
		tokenCreateCmd(configPath),
		tokenListCmd(configPath),
		tokenRevokeCmd(configPath),
	)
	return cmd
}

// openStateDB opens the database a command needs, without starting anything.
func openStateDB(ctx context.Context, configPath string) (*state.DB, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}

	db, err := state.Open(ctx, cfg.Canarium.DataDir)
	if err != nil {
		return nil, fmt.Errorf("opening state database at %s: %w", cfg.Canarium.DataDir, err)
	}
	return db, nil
}

func tokenCreateCmd(configPath *string) *cobra.Command {
	var scope string

	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create an API token",
		Args:  cobra.ExactArgs(1),
		Long: "Create an API token and print it once.\n\n" +
			"Only a digest is stored, so the token cannot be recovered later.\n" +
			"If you lose it, revoke it and create another.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.TrimSpace(args[0])
			if name == "" {
				return fmt.Errorf("token name cannot be empty")
			}

			if !slices.Contains(state.ValidScopes, scope) {
				return fmt.Errorf("invalid scope %q (must be one of: %s)",
					scope, strings.Join(state.ValidScopes, ", "))
			}

			ctx := cmd.Context()

			db, err := openStateDB(ctx, *configPath)
			if err != nil {
				return err
			}
			// Read-only command; a close error changes nothing for the caller.
			defer func() { _ = db.Close() }()

			raw := make([]byte, apiTokenBytes)
			if _, err := rand.Read(raw); err != nil {
				return fmt.Errorf("generating token: %w", err)
			}
			token := hex.EncodeToString(raw)

			digest := sha256.Sum256([]byte(token))
			if err := db.SaveAPIToken(ctx, hex.EncodeToString(digest[:]), name, scope); err != nil {
				if errors.Is(err, state.ErrTokenExists) {
					return fmt.Errorf("a token named %q already exists; revoke it first", name)
				}
				return err
			}

			fmt.Printf("Token %q created with %s scope.\n\n", name, scope)
			fmt.Printf("  %s\n\n", token)
			fmt.Println("This is the only time it will be shown. Use it as:")
			fmt.Printf("  curl -H 'Authorization: Bearer %s' http://localhost:8420/api/status\n", token)

			return nil
		},
	}

	cmd.Flags().StringVar(&scope, "scope", state.ScopeRead,
		"token scope: read (observe only) or admin (full control)")
	return cmd
}

func tokenListCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:          "list",
		Short:        "List API tokens",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			db, err := openStateDB(ctx, *configPath)
			if err != nil {
				return err
			}
			// Read-only command; a close error changes nothing for the caller.
			defer func() { _ = db.Close() }()

			tokens, err := db.ListAPITokens(ctx)
			if err != nil {
				return err
			}

			if len(tokens) == 0 {
				fmt.Println("No API tokens.")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tSCOPE\tCREATED\tLAST USED")
			for _, t := range tokens {
				lastUsed := "never"
				if t.LastUsed != nil {
					lastUsed = t.LastUsed.Format(time.RFC3339)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
					t.Name, t.Scope, t.CreatedAt.Format(time.RFC3339), lastUsed)
			}
			return w.Flush()
		},
	}
}

func tokenRevokeCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:          "revoke <name>",
		Short:        "Revoke an API token",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			db, err := openStateDB(ctx, *configPath)
			if err != nil {
				return err
			}
			// Read-only command; a close error changes nothing for the caller.
			defer func() { _ = db.Close() }()

			removed, err := db.DeleteAPIToken(ctx, args[0])
			if err != nil {
				return err
			}
			if !removed {
				return fmt.Errorf("no token named %q", args[0])
			}

			fmt.Printf("Token %q revoked.\n", args[0])
			return nil
		},
	}
}
