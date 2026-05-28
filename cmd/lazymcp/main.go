package main

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/s4na/lazymcp/internal/backend"
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
		Short: "Show configured backend servers",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			manager := backend.NewManager(os.Stderr)
			states := manager.States(cfg.ServerNames())
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tNAMESPACE\tSTATUS\tLAST_STARTED\tLAST_STOPPED\tSTOP_REASON\tLAST_ERROR\tCOMMAND")
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

	root.SetContext(context.Background())
	return root
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
