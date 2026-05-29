package migrate

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/s4na/lazymcp/internal/config"
)

type StatusOptions struct {
	ConfigPath      string
	CodexConfigPath string
}

type StatusReport struct {
	CodexCLI StatusSection
	CodexApp StatusSection
	LazyMCP  StatusSection
}

type StatusSection struct {
	Title    string
	Path     string
	Entries  []StatusEntry
	Warnings []string
}

type StatusEntry struct {
	Name      string
	Kind      string
	Transport string
	Status    string
	Details   string
}

func InspectStatus(opts StatusOptions) (*StatusReport, error) {
	if opts.ConfigPath == "" {
		opts.ConfigPath = config.DefaultPath()
	}
	if opts.CodexConfigPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		opts.CodexConfigPath = filepath.Join(home, ".codex", "config.toml")
	}
	codexDir := filepath.Dir(opts.CodexConfigPath)
	report := &StatusReport{
		CodexCLI: inspectCodexCLIStatus(opts.CodexConfigPath),
		CodexApp: inspectCodexAppStatus(codexDir),
		LazyMCP:  inspectLazyMCPStatus(opts.ConfigPath),
	}
	return report, nil
}

func inspectCodexCLIStatus(path string) StatusSection {
	section := StatusSection{Title: "Codex CLI", Path: path}
	raw, err := readCodexConfig(path)
	if err != nil {
		section.Warnings = append(section.Warnings, statusReadWarning(path, err))
		return section
	}
	for _, name := range sortedClientServerNames(raw.MCPServers) {
		srv := raw.MCPServers[name]
		section.Entries = append(section.Entries, StatusEntry{
			Name:      name,
			Kind:      codexCLIKind(srv),
			Transport: clientTransport(srv),
			Status:    clientStatus(srv),
			Details:   clientDetails(srv),
		})
	}
	return section
}

func inspectCodexAppStatus(codexDir string) StatusSection {
	section := StatusSection{Title: "Codex App", Path: codexDir}
	pluginEntries, pluginWarnings := codexPluginStatusEntries(codexDir)
	section.Entries = append(section.Entries, pluginEntries...)
	section.Warnings = append(section.Warnings, pluginWarnings...)
	connectorEntries, connectorWarnings := codexAppConnectorStatusEntries(codexDir)
	section.Entries = append(section.Entries, connectorEntries...)
	section.Warnings = append(section.Warnings, connectorWarnings...)
	sortStatusEntries(section.Entries)
	sort.Strings(section.Warnings)
	return section
}

func codexPluginStatusEntries(codexDir string) ([]StatusEntry, []string) {
	pattern := filepath.Join(codexDir, ".tmp", "plugins", "plugins", "*", ".mcp.json")
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return nil, []string{err.Error()}
	}
	sort.Strings(paths)
	var entries []StatusEntry
	var warnings []string
	for _, path := range paths {
		manifest, err := readPluginMCPManifest(path)
		if err != nil {
			warnings = append(warnings, statusReadWarning(path, err))
			continue
		}
		for _, name := range sortedClientServerNames(manifest.MCPServers) {
			srv := manifest.MCPServers[name]
			entries = append(entries, StatusEntry{
				Name:      name,
				Kind:      "plugin",
				Transport: clientTransport(srv),
				Status:    pluginStatus(srv),
				Details:   clientDetails(srv),
			})
		}
	}
	return entries, warnings
}

func codexAppConnectorStatusEntries(codexDir string) ([]StatusEntry, []string) {
	pattern := filepath.Join(codexDir, "cache", "codex_apps_tools", "*.json")
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return nil, []string{err.Error()}
	}
	sort.Strings(paths)
	connectors := map[string]appConnectorSummary{}
	var warnings []string
	for _, path := range paths {
		cache, err := readAppToolsCache(path)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: Codex App tool cache could not be read: %v", path, err))
			continue
		}
		for _, tool := range cache.Tools {
			key := appToolConnectorKey(tool)
			if key == "" {
				continue
			}
			toolName := appToolName(tool)
			toolKey := key + "\x00" + toolName
			summary := connectors[key]
			if summary.Name == "" {
				summary.Name = appToolConnectorName(tool)
			}
			if summary.Tools == nil {
				summary.Tools = map[string]struct{}{}
			}
			summary.Tools[toolKey] = struct{}{}
			connectors[key] = summary
		}
	}
	var entries []StatusEntry
	for id, summary := range connectors {
		name := summary.Name
		if name == "" {
			name = id
		}
		entries = append(entries, StatusEntry{
			Name:      name,
			Kind:      "connector",
			Transport: "remote",
			Status:    "not importable",
			Details:   fmt.Sprintf("%d cached tools; no local stdio MCP command", len(summary.Tools)),
		})
	}
	return entries, warnings
}

func inspectLazyMCPStatus(path string) StatusSection {
	section := StatusSection{Title: "lazymcp", Path: path}
	cfg, err := readLazyConfig(path)
	if err != nil {
		section.Warnings = append(section.Warnings, statusReadWarning(path, err))
		return section
	}
	for _, name := range sortedConfigServerNames(cfg.Servers) {
		srv := cfg.Servers[name]
		section.Entries = append(section.Entries, StatusEntry{
			Name:      name,
			Kind:      "server",
			Transport: "stdio",
			Status:    "configured",
			Details:   fmt.Sprintf("namespace=%s command=%s", srv.NamespaceOrName(name), maskedCommandLine(srv)),
		})
	}
	return section
}

func FormatStatus(report *StatusReport) string {
	var b strings.Builder
	formatStatusSection(&b, report.CodexCLI)
	formatStatusSection(&b, report.CodexApp)
	formatStatusSection(&b, report.LazyMCP)
	return b.String()
}

func formatStatusSection(b *strings.Builder, section StatusSection) {
	fmt.Fprintf(b, "%s (%s)\n", section.Title, section.Path)
	if len(section.Warnings) > 0 {
		fmt.Fprintln(b, "warnings:")
		for _, warning := range section.Warnings {
			fmt.Fprintf(b, "  - %s\n", warning)
		}
	}
	if len(section.Entries) == 0 {
		fmt.Fprintln(b, "  (none)")
		fmt.Fprintln(b)
		return
	}
	var table bytes.Buffer
	w := tabwriter.NewWriter(&table, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tKIND\tTRANSPORT\tSTATUS\tDETAILS")
	for _, entry := range section.Entries {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", entry.Name, entry.Kind, entry.Transport, entry.Status, entry.Details)
	}
	_ = w.Flush()
	b.Write(table.Bytes())
	fmt.Fprintln(b)
}

func sortedClientServerNames(servers map[string]clientServer) []string {
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sortedConfigServerNames(servers map[string]config.Server) []string {
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sortStatusEntries(entries []StatusEntry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Kind != entries[j].Kind {
			return entries[i].Kind < entries[j].Kind
		}
		return entries[i].Name < entries[j].Name
	})
}

func codexCLIKind(srv clientServer) string {
	if isLazyMCPProxy(srv) {
		return "proxy"
	}
	return "server"
}

func clientTransport(srv clientServer) string {
	if srv.Type != "" {
		return srv.Type
	}
	if srv.URL != "" {
		return "remote"
	}
	if srv.Command != "" {
		return "stdio"
	}
	return "unknown"
}

func clientStatus(srv clientServer) string {
	if isLazyMCPProxy(srv) {
		return "points to lazymcp"
	}
	if srv.Command == "" {
		if srv.Type != "" || srv.URL != "" {
			return "not importable"
		}
		return "invalid"
	}
	if srv.Type != "" && srv.Type != "stdio" {
		return "not importable"
	}
	return "importable"
}

func pluginStatus(srv clientServer) string {
	if srv.Command == "" {
		if srv.Type != "" || srv.URL != "" {
			return "not importable"
		}
		return "invalid"
	}
	if srv.Type != "" && srv.Type != "stdio" {
		return "not importable"
	}
	return "importable"
}

func clientDetails(srv clientServer) string {
	if srv.Command != "" {
		return maskedClientCommandLine(srv)
	}
	if srv.URL != "" {
		return redactURL(srv.URL)
	}
	if srv.Type != "" {
		return "remote transport without local command"
	}
	return "missing command"
}

func maskedClientCommandLine(srv clientServer) string {
	return strings.Join(append([]string{srv.Command}, maskArgs(srv.Args)...), " ")
}

func statusReadWarning(path string, err error) string {
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Sprintf("%s: not found", path)
	}
	return err.Error()
}

func redactURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "<redacted-url>"
	}
	if parsed.User != nil {
		parsed.User = url.User("<redacted>")
	}
	query := parsed.Query()
	for key, values := range query {
		if isSecretURLQueryKey(key) {
			for i := range values {
				values[i] = "<redacted>"
			}
			query[key] = values
		}
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func isSecretURLQueryKey(key string) bool {
	normalized := strings.ToUpper(strings.NewReplacer("-", "", "_", "").Replace(key))
	return normalized == "KEY" ||
		strings.Contains(normalized, "TOKEN") ||
		strings.Contains(normalized, "SECRET") ||
		strings.Contains(normalized, "PASSWORD") ||
		strings.Contains(normalized, "APIKEY") ||
		strings.Contains(normalized, "ACCESSKEY")
}

func appToolConnectorKey(tool appToolCacheEntry) string {
	if tool.ConnectorID != "" {
		return tool.ConnectorID
	}
	if tool.Tool.Meta.ConnectorID != "" {
		return tool.Tool.Meta.ConnectorID
	}
	if tool.ConnectorName != "" {
		return tool.ConnectorName
	}
	if tool.Tool.Meta.ConnectorName != "" {
		return tool.Tool.Meta.ConnectorName
	}
	return tool.ServerName
}

func appToolConnectorName(tool appToolCacheEntry) string {
	if tool.ConnectorName != "" {
		return tool.ConnectorName
	}
	if tool.Tool.Meta.ConnectorName != "" {
		return tool.Tool.Meta.ConnectorName
	}
	return tool.ServerName
}

func appToolName(tool appToolCacheEntry) string {
	if tool.ToolName != "" {
		return tool.ToolName
	}
	return tool.Tool.Name
}
