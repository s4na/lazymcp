package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
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
		Use:   "serve",
		Short: "Run the MCP stdio proxy",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			srv := server.New(cfg, os.Stdin, os.Stdout, os.Stderr)
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

	root.AddCommand(newMigrateCommand(&configPath))

	root.SetContext(context.Background())
	return root
}

func newMigrateCommand(configPath *string) *cobra.Command {
	var opts migrate.Options
	var dryRun bool
	var yes bool
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Migrate existing client MCP settings into lazymcp config",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = cmd.Help()
			return fmt.Errorf("migrate requires a source subcommand")
		},
	}
	cmd.PersistentFlags().BoolVar(&opts.Write, "write", false, "write merged lazymcp config")
	cmd.PersistentFlags().BoolVar(&opts.Overwrite, "overwrite", false, "overwrite existing lazymcp server entries")
	cmd.PersistentFlags().StringVar(&opts.SourcePath, "source-path", "", "source client config path")
	cmd.PersistentFlags().BoolVar(&dryRun, "dry-run", false, "preview the migration without writing files (default unless --write is set)")
	cmd.PersistentFlags().BoolVar(&opts.UpdateClient, "register-client", false, "replace the source client MCP settings with the lazymcp proxy after importing")
	cmd.PersistentFlags().BoolVarP(&yes, "yes", "y", false, "write files and answer yes to registering lazymcp in the source client")

	cmd.AddCommand(&cobra.Command{
		Use:   "codex",
		Short: "Migrate Codex MCP settings",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if yes {
				opts.Write = true
				opts.UpdateClient = true
			}
			if dryRun && opts.Write {
				return fmt.Errorf("--dry-run and --write cannot be used together")
			}
			opts.Source = migrate.SourceCodex
			opts.ConfigPath = *configPath
			if opts.Write && !opts.UpdateClient && promptRegisterClient(cmd.InOrStdin(), cmd.OutOrStdout()) {
				opts.UpdateClient = true
			}
			plan, err := migrate.Run(opts)
			if plan != nil {
				fmt.Fprint(cmd.OutOrStdout(), migrate.FormatPlan(plan))
			}
			return err
		},
	})
	return cmd
}

func promptRegisterClient(in io.Reader, out io.Writer) bool {
	if !shouldPrompt(in) {
		return false
	}
	fmt.Fprint(out, "Register lazymcp as the only MCP server in the source client? [y/N] ")
	answer, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && len(answer) == 0 {
		return false
	}
	answer = strings.TrimSpace(strings.ToLower(answer))
	return answer == "y" || answer == "yes"
}

func shouldPrompt(in io.Reader) bool {
	file, ok := in.(*os.File)
	if !ok {
		return true
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
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
