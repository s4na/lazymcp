package main

import (
	"context"
	"fmt"
	"os"

	"github.com/s4na/lazymcp/internal/config"
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

	root.SetContext(context.Background())
	return root
}
