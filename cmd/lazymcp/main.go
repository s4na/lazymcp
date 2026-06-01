package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/s4na/lazymcp/internal/backend"
	"github.com/s4na/lazymcp/internal/config"
	"github.com/s4na/lazymcp/internal/migrate"
	"github.com/s4na/lazymcp/internal/server"
	"github.com/spf13/cobra"
)

func main() {
	if err := newRootCommand().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newRootCommand() *cobra.Command {
	var configPath string

	root := &cobra.Command{
		Use:   "lazymcp",
		Short: "Lazy MCP proxy/router",
	}
	root.PersistentFlags().StringVarP(&configPath, "config", "c", config.DefaultPath(), "config file path")

	root.AddCommand(&cobra.Command{
		Use:    "h [command]",
		Short:  "Help about any command",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			target := root
			if len(args) > 0 {
				found, _, err := root.Find(args)
				if err != nil {
					return err
				}
				target = found
			}
			target.InitDefaultHelpFlag()
			target.InitDefaultVersionFlag()
			return target.Help()
		},
	})

	root.AddCommand(&cobra.Command{
		Use:   "init",
		Short: "Create an empty lazymcp config file for migration or manual editing",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := initConfig(configPath); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "created config: %s\n", configPath)
			return nil
		},
	})

	root.AddCommand(&cobra.Command{
		Use:   "serve",
		Short: "Run the MCP stdio proxy",
		RunE: func(cmd *cobra.Command, args []string) error {
			logFile, err := openLogFile()
			if err != nil {
				return err
			}
			defer logFile.Close()
			stderr := io.MultiWriter(os.Stderr, logFile)
			fmt.Fprintf(logFile, "%s lazymcp serve starting with config %s\n", time.Now().Format(time.RFC3339), configPath)
			cfg, err := config.Load(configPath)
			if err != nil {
				fmt.Fprintf(logFile, "%s lazymcp serve failed to load config: %s\n", time.Now().Format(time.RFC3339), err)
				return err
			}
			srv := server.New(cfg, os.Stdin, os.Stdout, stderr)
			return srv.Run(cmd.Context())
		},
	})

	root.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List cached tools",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			for _, tool := range cfg.Tools() {
				fmt.Fprintln(cmd.OutOrStdout(), tool.Name)
			}
			return nil
		},
	})

	root.AddCommand(&cobra.Command{
		Use:   "inspect",
		Short: "Show configured backend servers with initial lifecycle columns",
		Long: "Show configured backend servers with lifecycle columns for this inspect process.\n" +
			"Standalone inspect cannot read live in-memory state from an already-running lazymcp serve session.",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			manager := backend.NewManager(os.Stderr)
			states := manager.States(cfg.ServerNames())
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tNAMESPACE\tSTATUS\tLAST_STARTED\tLAST_STOPPED\tSTOP_REASON\tLAST_ERROR\tCOMMAND_LINE")
			for _, name := range cfg.ServerNames() {
				srv := cfg.Servers[name]
				state := states[name]
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					name,
					srv.NamespaceOrName(name),
					state.Status,
					formatTime(state.LastStarted),
					formatTime(state.LastStopped),
					formatStopReason(state.StopReason),
					formatEmpty(state.LastError),
					srv.CommandLine(),
				)
			}
			return w.Flush()
		},
	})

	root.AddCommand(newStatusCommand(&configPath))
	root.AddCommand(newMigrateCommand(&configPath))

	root.SetContext(context.Background())
	return root
}

func newStatusCommand(configPath *string) *cobra.Command {
	var codexConfigPath string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show MCP settings discovered in Codex CLI, Codex App, and lazymcp",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			report, err := migrate.InspectStatus(migrate.StatusOptions{
				ConfigPath:      *configPath,
				CodexConfigPath: codexConfigPath,
			})
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), migrate.FormatStatus(report))
			return nil
		},
	}
	cmd.Flags().StringVar(&codexConfigPath, "codex-config", "", "Codex config.toml path")
	return cmd
}

func newMigrateCommand(configPath *string) *cobra.Command {
	var opts migrate.Options
	var dryRun bool
	var yes bool
	runCodexMigration := func(cmd *cobra.Command) error {
		if yes {
			opts.Write = true
		}
		if dryRun && opts.Write {
			return fmt.Errorf("--dry-run and --write cannot be used together")
		}
		if opts.Diff && opts.Write {
			return fmt.Errorf("--diff and --write cannot be used together")
		}
		if opts.DiscoverTools && !opts.Write && !opts.Diff {
			return fmt.Errorf("--discover-tools requires --write or --diff")
		}
		opts.Source = migrate.SourceCodex
		opts.ConfigPath = *configPath
		opts.UpdateClient = opts.UpdateClient || opts.Write || opts.Diff
		plan, err := migrate.Run(opts)
		if plan != nil {
			fmt.Fprint(cmd.OutOrStdout(), migrate.FormatPlan(plan))
		}
		return err
	}
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Migrate Codex MCP settings into lazymcp config",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCodexMigration(cmd)
		},
	}
	cmd.PersistentFlags().BoolVar(&opts.Write, "write", false, "write lazymcp config and update Codex MCP settings")
	cmd.PersistentFlags().BoolVar(&opts.Diff, "diff", false, "show the lazymcp and Codex config changes as a unified diff without writing files")
	cmd.PersistentFlags().BoolVar(&opts.Overwrite, "overwrite", false, "overwrite existing lazymcp server entries")
	cmd.PersistentFlags().StringVar(&opts.SourcePath, "source-path", "", "source client config path")
	cmd.PersistentFlags().BoolVar(&opts.DiscoverTools, "discover-tools", false, "start imported stdio MCP servers once and write discovered tools into lazymcp config")
	cmd.PersistentFlags().BoolVar(&dryRun, "dry-run", false, "preview the migration without writing files (default unless --write is set)")
	cmd.PersistentFlags().BoolVar(&opts.UpdateClient, "register-client", false, "replace the source client MCP settings with the lazymcp proxy after importing")
	cmd.PersistentFlags().BoolVarP(&yes, "yes", "y", false, "write lazymcp config and update Codex MCP settings")
	_ = cmd.PersistentFlags().MarkHidden("register-client")

	cmd.AddCommand(&cobra.Command{
		Use:    "codex",
		Short:  "Migrate Codex MCP settings",
		Args:   cobra.NoArgs,
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCodexMigration(cmd)
		},
	})
	return cmd
}

func initConfig(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("config already exists: %s", path)
		}
		return err
	}
	_, writeErr := file.WriteString("servers: {}\n")
	if closeErr := file.Close(); writeErr == nil {
		writeErr = closeErr
	}
	return writeErr
}

func openLogFile() (*os.File, error) {
	path, err := defaultLogPath()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func defaultLogPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "tmp", "lazymcp", "lazymcp.log"), nil
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format(time.RFC3339)
}

func formatStopReason(reason backend.StopReason) string {
	if reason == "" {
		return "-"
	}
	return string(reason)
}

func formatEmpty(value string) string {
	if value == "" {
		return "-"
	}
	return value
}
