package main

import (
	"context"
	"fmt"
	"os"

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
		Short: "Show configured backend servers",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			for _, name := range cfg.ServerNames() {
				srv := cfg.Servers[name]
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", name, srv.NamespaceOrName(name), srv.CommandLine())
			}
			return nil
		},
	})

	root.AddCommand(newMigrateCommand(&configPath))

	root.SetContext(context.Background())
	return root
}

func newMigrateCommand(configPath *string) *cobra.Command {
	var opts migrate.Options
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Migrate existing client MCP settings into lazymcp config",
	}
	cmd.PersistentFlags().BoolVar(&opts.Write, "write", false, "write merged lazymcp config")
	cmd.PersistentFlags().BoolVar(&opts.Overwrite, "overwrite", false, "overwrite existing lazymcp server entries")
	cmd.PersistentFlags().StringVar(&opts.SourcePath, "source-path", "", "source client config path")
	cmd.PersistentFlags().BoolVar(&dryRun, "dry-run", false, "preview the migration without writing files")

	cmd.AddCommand(&cobra.Command{
		Use:   "codex",
		Short: "Migrate Codex MCP settings",
		RunE: func(cmd *cobra.Command, args []string) error {
			if dryRun && opts.Write {
				return fmt.Errorf("--dry-run and --write cannot be used together")
			}
			opts.Source = migrate.SourceCodex
			opts.ConfigPath = *configPath
			plan, err := migrate.Run(opts)
			if plan != nil {
				fmt.Fprint(cmd.OutOrStdout(), migrate.FormatPlan(plan))
			}
			return err
		},
	})
	return cmd
}
