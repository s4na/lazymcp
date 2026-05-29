package migrate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/s4na/lazymcp/internal/config"
	"gopkg.in/yaml.v3"
)

type Source string

const (
	SourceCodex Source = "codex"
)

type Options struct {
	Source       Source
	ConfigPath   string
	SourcePath   string
	Write        bool
	Diff         bool
	Overwrite    bool
	UpdateClient bool
}

type Plan struct {
	Source       Source
	ConfigPath   string
	SourceFiles  []string
	Servers      map[string]config.Server
	Conflicts    []string
	Skipped      []string
	Blocked      []string
	ChangedFiles []string
	Backups      []string
	Diffs        []FileDiff
}

type FileDiff struct {
	Path string
	Diff string
}

type clientConfig struct {
	MCPServers map[string]clientServer
	Skipped    []string
}

type clientServer struct {
	Type    string            `json:"type" toml:"type"`
	Command string            `json:"command" toml:"command"`
	Args    []string          `json:"args" toml:"args"`
	Env     map[string]string `json:"env" toml:"env"`
	URL     string            `json:"url" toml:"url"`
}

var nowUTC = func() time.Time {
	return time.Now().UTC()
}

func Run(opts Options) (*Plan, error) {
	if opts.ConfigPath == "" {
		opts.ConfigPath = config.DefaultPath()
	}
	servers, sourceFiles, skipped, blocked, err := discover(opts)
	if err != nil {
		return nil, err
	}
	plan := &Plan{
		Source:      opts.Source,
		ConfigPath:  opts.ConfigPath,
		SourceFiles: sourceFiles,
		Servers:     servers,
		Skipped:     skipped,
		Blocked:     blocked,
	}
	if len(servers) == 0 && !hasExistingLazyProxySkip(skipped) {
		return plan, fmt.Errorf("no importable Codex MCP servers found from %s; lazymcp imports Codex config.toml [mcp_servers.*] entries and stdio plugin .mcp.json servers, but remote Codex App connectors/plugins may be managed separately", primarySourceFile(sourceFiles))
	}
	if err := validateExistingLazyConfigFile(opts.ConfigPath); err != nil {
		return plan, err
	}
	conflicts, err := mergeConflicts(opts.ConfigPath, servers, opts.Overwrite)
	if err != nil {
		return nil, err
	}
	plan.Conflicts = conflicts
	if len(conflicts) > 0 {
		return plan, fmt.Errorf("migration has conflicts: %s", strings.Join(conflicts, "; "))
	}
	if opts.UpdateClient && len(plan.Blocked) > 0 {
		return plan, fmt.Errorf("updating the source client would remove unsupported Codex MCP servers: %s", strings.Join(plan.Blocked, "; "))
	}
	if opts.Diff {
		diffs, err := diffConfig(opts.ConfigPath, servers, opts.Overwrite)
		if err != nil {
			return plan, err
		}
		plan.Diffs = append(plan.Diffs, diffs...)
		if opts.UpdateClient {
			if len(sourceFiles) == 0 {
				return plan, fmt.Errorf("no source client config path discovered")
			}
			diffs, err := diffClientProxy(opts.Source, sourceFiles[0], opts.ConfigPath)
			if err != nil {
				return plan, err
			}
			plan.Diffs = append(plan.Diffs, diffs...)
		}
		return plan, nil
	}
	if opts.Write {
		changed, backups, err := writeConfig(opts.ConfigPath, servers, opts.Overwrite)
		if err != nil {
			return plan, err
		}
		plan.ChangedFiles = changed
		plan.Backups = backups
		if err := validateWrittenLazyConfigFile(opts.ConfigPath); err != nil {
			return plan, err
		}
	}
	if opts.UpdateClient {
		if !opts.Write {
			return plan, fmt.Errorf("updating the source client requires --write")
		}
		if len(sourceFiles) == 0 {
			return plan, fmt.Errorf("no source client config path discovered")
		}
		changed, backups, err := writeClientProxy(opts.Source, sourceFiles[0], opts.ConfigPath)
		if err != nil {
			return plan, err
		}
		plan.ChangedFiles = append(plan.ChangedFiles, changed...)
		plan.Backups = append(plan.Backups, backups...)
		if _, err := readCodexConfig(sourceFiles[0]); err != nil {
			return plan, err
		}
	}
	return plan, nil
}

func primarySourceFile(sourceFiles []string) string {
	if len(sourceFiles) == 0 {
		return "Codex config"
	}
	return sourceFiles[0]
}

func discover(opts Options) (map[string]config.Server, []string, []string, []string, error) {
	switch opts.Source {
	case SourceCodex:
		return discoverCodex(opts)
	default:
		return nil, nil, nil, nil, fmt.Errorf("unsupported source %q", opts.Source)
	}
}

func writeClientProxy(source Source, sourcePath, configPath string) ([]string, []string, error) {
	switch source {
	case SourceCodex:
		return writeCodexProxy(sourcePath, configPath)
	default:
		return nil, nil, fmt.Errorf("unsupported source %q", source)
	}
}

func diffClientProxy(source Source, sourcePath, configPath string) ([]FileDiff, error) {
	switch source {
	case SourceCodex:
		before, after, changed, err := codexProxyData(sourcePath, configPath)
		if err != nil {
			return nil, err
		}
		if !changed {
			return nil, nil
		}
		before, after, err = redactCodexDiffData(before, after)
		if err != nil {
			return nil, err
		}
		return []FileDiff{{Path: sourcePath, Diff: UnifiedDiff(sourcePath, sourcePath, before, after)}}, nil
	default:
		return nil, fmt.Errorf("unsupported source %q", source)
	}
}

func writeCodexProxy(sourcePath, configPath string) ([]string, []string, error) {
	_, after, changed, err := codexProxyData(sourcePath, configPath)
	if err != nil {
		return nil, nil, err
	}
	if !changed {
		return nil, nil, nil
	}
	var backups []string
	backup, err := backupFile(sourcePath)
	if err != nil {
		return nil, nil, err
	}
	backups = append(backups, backup)
	if err := writeConfigFile(sourcePath, after); err != nil {
		return nil, nil, err
	}
	return []string{sourcePath}, backups, nil
}

func codexProxyData(sourcePath, configPath string) ([]byte, []byte, bool, error) {
	before, err := os.ReadFile(sourcePath)
	if err != nil {
		return nil, nil, false, err
	}
	var raw map[string]any
	if err := toml.Unmarshal(before, &raw); err != nil {
		return nil, nil, false, fmt.Errorf("parse %s: %w", sourcePath, err)
	}
	if codexAlreadyUsesLazyProxy(raw, configPath) {
		return before, before, false, nil
	}
	raw["mcp_servers"] = map[string]any{
		"lazymcp": map[string]any{
			"command": "lazymcp",
			"args":    []string{"serve", "--config", absoluteOrOriginal(configPath)},
		},
	}
	encoded, err := toml.Marshal(raw)
	if err != nil {
		return nil, nil, false, err
	}
	return before, encoded, string(before) != string(encoded), nil
}

func codexAlreadyUsesLazyProxy(raw map[string]any, configPath string) bool {
	serversValue, ok := raw["mcp_servers"]
	if !ok {
		return false
	}
	servers, ok := serversValue.(map[string]any)
	if !ok || len(servers) != 1 {
		return false
	}
	value, ok := servers["lazymcp"]
	if !ok {
		return false
	}
	table, ok := value.(map[string]any)
	if !ok {
		return false
	}
	srv, err := parseCodexServer("", "lazymcp", table)
	if err != nil {
		return false
	}
	wantConfigPath := absoluteOrOriginal(configPath)
	gotConfigPath, ok := lazyProxyConfigPath(srv.Args)
	return isLazyMCPProxy(srv) && ok && samePath(gotConfigPath, wantConfigPath)
}

func absoluteOrOriginal(path string) string {
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}

func discoverCodex(opts Options) (map[string]config.Server, []string, []string, []string, error) {
	path := opts.SourcePath
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, nil, nil, nil, err
		}
		path = filepath.Join(home, ".codex", "config.toml")
	}
	raw, err := readCodexConfig(path)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	mcpServers := raw.MCPServers
	sourceFiles := []string{path}
	skipped := append([]string(nil), raw.Skipped...)
	blocked := []string{}
	pluginServers, pluginFiles, pluginSkipped, err := readCodexPluginMCPManifests(filepath.Dir(path), mcpServers)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	for name, srv := range pluginServers {
		mcpServers[name] = srv
	}
	sourceFiles = append(sourceFiles, pluginFiles...)
	skipped = append(skipped, pluginSkipped...)
	appCacheFiles, appCacheSkipped, err := readCodexAppToolCacheSkips(filepath.Dir(path))
	if err != nil {
		return nil, nil, nil, nil, err
	}
	sourceFiles = append(sourceFiles, appCacheFiles...)
	skipped = append(skipped, appCacheSkipped...)
	servers, convertedSkipped := convert(mcpServers)
	skipped = append(skipped, convertedSkipped...)
	blocked = append(blocked, directCodexBlockedSkips(convertedSkipped)...)
	sort.Strings(skipped)
	sort.Strings(blocked)
	return servers, sourceFiles, skipped, blocked, nil
}

func directCodexBlockedSkips(skipped []string) []string {
	var blocked []string
	for _, item := range skipped {
		if strings.Contains(item, "Codex MCP server uses unsupported") || strings.Contains(item, "Codex MCP server has no command") {
			blocked = append(blocked, item)
		}
	}
	return blocked
}

func hasExistingLazyProxySkip(skipped []string) bool {
	for _, item := range skipped {
		if strings.Contains(item, "already points to lazymcp") {
			return true
		}
	}
	return false
}

func readCodexPluginMCPManifests(codexDir string, existing map[string]clientServer) (map[string]clientServer, []string, []string, error) {
	pattern := filepath.Join(codexDir, ".tmp", "plugins", "plugins", "*", ".mcp.json")
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return nil, nil, nil, err
	}
	sort.Strings(paths)
	servers := map[string]clientServer{}
	var sourceFiles []string
	var skipped []string
	for _, path := range paths {
		manifest, err := readPluginMCPManifest(path)
		if err != nil {
			return nil, nil, nil, err
		}
		sourceFiles = append(sourceFiles, path)
		for name, srv := range manifest.MCPServers {
			if _, ok := existing[name]; ok {
				skipped = append(skipped, fmt.Sprintf("%s: plugin MCP manifest duplicates Codex config entry", name))
				continue
			}
			if _, ok := servers[name]; ok {
				skipped = append(skipped, fmt.Sprintf("%s: duplicate plugin MCP manifest entry", name))
				continue
			}
			if srv.Command == "" {
				if srv.Type != "" || srv.URL != "" {
					skipped = append(skipped, fmt.Sprintf("%s: plugin MCP manifest uses unsupported remote transport", name))
					continue
				}
				skipped = append(skipped, fmt.Sprintf("%s: plugin MCP manifest has no command", name))
				continue
			}
			if srv.Type != "" && srv.Type != "stdio" {
				skipped = append(skipped, fmt.Sprintf("%s: plugin MCP manifest uses unsupported %q transport", name, srv.Type))
				continue
			}
			servers[name] = srv
		}
	}
	sort.Strings(skipped)
	return servers, sourceFiles, skipped, nil
}

type pluginMCPManifest struct {
	MCPServers map[string]clientServer `json:"mcpServers"`
}

func readPluginMCPManifest(path string) (*pluginMCPManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var manifest pluginMCPManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if manifest.MCPServers == nil {
		manifest.MCPServers = map[string]clientServer{}
	}
	return &manifest, nil
}

func readCodexAppToolCacheSkips(codexDir string) ([]string, []string, error) {
	pattern := filepath.Join(codexDir, "cache", "codex_apps_tools", "*.json")
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return nil, nil, err
	}
	sort.Strings(paths)
	connectors := map[string]appConnectorSummary{}
	var sourceFiles []string
	var skipped []string
	for _, path := range paths {
		cache, err := readAppToolsCache(path)
		if err != nil {
			sourceFiles = append(sourceFiles, path)
			skipped = append(skipped, fmt.Sprintf("%s: Codex App tool cache could not be read for skipped connector diagnostics: %v", path, err))
			continue
		}
		sourceFiles = append(sourceFiles, path)
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
	for id, summary := range connectors {
		name := summary.Name
		if name == "" {
			name = id
		}
		skipped = append(skipped, fmt.Sprintf("%s: Codex App connector cache has %d tools but no local stdio MCP command to import", name, len(summary.Tools)))
	}
	sort.Strings(skipped)
	return sourceFiles, skipped, nil
}

type appToolsCache struct {
	Tools []appToolCacheEntry `json:"tools"`
}

type appToolCacheEntry struct {
	ServerName    string `json:"server_name"`
	ToolName      string `json:"tool_name"`
	ConnectorID   string `json:"connector_id"`
	ConnectorName string `json:"connector_name"`
	Tool          struct {
		Name string `json:"name"`
		Meta struct {
			ConnectorID   string `json:"connector_id"`
			ConnectorName string `json:"connector_name"`
		} `json:"_meta"`
	} `json:"tool"`
}

type appConnectorSummary struct {
	Name  string
	Tools map[string]struct{}
}

func readAppToolsCache(path string) (*appToolsCache, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cache appToolsCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &cache, nil
}

func convert(servers map[string]clientServer) (map[string]config.Server, []string) {
	out := map[string]config.Server{}
	var skipped []string
	for name, srv := range servers {
		if isLazyMCPProxy(srv) {
			skipped = append(skipped, fmt.Sprintf("%s: already points to lazymcp", name))
			continue
		}
		if srv.Command == "" {
			if srv.Type != "" || srv.URL != "" {
				skipped = append(skipped, fmt.Sprintf("%s: Codex MCP server uses unsupported remote transport", name))
				continue
			}
			skipped = append(skipped, fmt.Sprintf("%s: Codex MCP server has no command", name))
			continue
		}
		if srv.Type != "" && srv.Type != "stdio" {
			skipped = append(skipped, fmt.Sprintf("%s: Codex MCP server uses unsupported %q transport", name, srv.Type))
			continue
		}
		out[name] = config.Server{
			Command:        srv.Command,
			Args:           append([]string(nil), srv.Args...),
			Env:            copyEnv(srv.Env),
			Namespace:      name,
			IdleTimeout:    config.Duration(5 * time.Minute),
			RequestTimeout: config.Duration(10 * time.Minute),
			Tools:          []config.Tool{},
		}
	}
	sort.Strings(skipped)
	return out, skipped
}

func mergeConflicts(path string, incoming map[string]config.Server, overwrite bool) ([]string, error) {
	existing, err := readLazyConfig(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var conflicts []string
	namespaces := map[string]string{}
	for name, srv := range existing.Servers {
		if isLazyConfigServer(srv, path) {
			continue
		}
		namespaces[srv.NamespaceOrName(name)] = name
	}
	for name, srv := range incoming {
		if existingSrv, ok := existing.Servers[name]; ok && isLazyConfigServer(existingSrv, path) {
			continue
		}
		if existingSrv, ok := existing.Servers[name]; ok && !overwrite && !sameImportedServer(name, existingSrv, srv) {
			conflicts = append(conflicts, fmt.Sprintf("server name %q already exists", name))
		}
		namespace := srv.NamespaceOrName(name)
		if existingName, ok := namespaces[namespace]; ok && existingName != name {
			conflicts = append(conflicts, fmt.Sprintf("namespace %q already exists on server %q", namespace, existingName))
		}
	}
	sort.Strings(conflicts)
	return conflicts, nil
}

func writeConfig(path string, incoming map[string]config.Server, overwrite bool) ([]string, []string, error) {
	_, after, changed, err := configData(path, incoming, overwrite)
	if err != nil {
		return nil, nil, err
	}
	if !changed {
		return nil, nil, nil
	}
	var backups []string
	if _, err := os.Stat(path); err == nil {
		backup, err := backupFile(path)
		if err != nil {
			return nil, nil, err
		}
		backups = append(backups, backup)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, nil, err
	}
	if err := writeConfigFile(path, after); err != nil {
		return nil, nil, err
	}
	return []string{path}, backups, nil
}

func diffConfig(path string, incoming map[string]config.Server, overwrite bool) ([]FileDiff, error) {
	before, after, changed, err := configData(path, incoming, overwrite)
	if err != nil {
		return nil, err
	}
	if !changed {
		return nil, nil
	}
	before, after, err = redactLazyDiffData(before, after)
	if err != nil {
		return nil, err
	}
	return []FileDiff{{Path: path, Diff: UnifiedDiff(path, path, before, after)}}, nil
}

func redactCodexDiffData(before, after []byte) ([]byte, []byte, error) {
	redactedBefore, err := redactCodexConfigData(before)
	if err != nil {
		return nil, nil, err
	}
	redactedAfter, err := redactCodexConfigData(after)
	if err != nil {
		return nil, nil, err
	}
	return redactedBefore, redactedAfter, nil
}

func redactCodexConfigData(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return data, nil
	}
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	return toml.Marshal(redactConfigValue("", raw))
}

func redactLazyDiffData(before, after []byte) ([]byte, []byte, error) {
	redactedBefore, err := redactLazyConfigData(before)
	if err != nil {
		return nil, nil, err
	}
	redactedAfter, err := redactLazyConfigData(after)
	if err != nil {
		return nil, nil, err
	}
	return redactedBefore, redactedAfter, nil
}

func redactLazyConfigData(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return data, nil
	}
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	return yaml.Marshal(redactConfigValue("", raw))
}

func redactConfigValue(key string, value any) any {
	return redactConfigValueIn(key, "", value)
}

func redactConfigValueIn(key, parentKey string, value any) any {
	if isSecretKey(key) {
		return "<redacted>"
	}
	switch typed := value.(type) {
	case map[string]any:
		if strings.EqualFold(key, "env") {
			return maskAnyEnv(typed)
		}
		out := make(map[string]any, len(typed))
		for childKey, childValue := range typed {
			if isServerCollectionKey(key) {
				out[childKey] = redactConfigValueIn("", key, childValue)
			} else {
				out[childKey] = redactConfigValueIn(childKey, key, childValue)
			}
		}
		return out
	case []any:
		if strings.EqualFold(key, "args") {
			return maskAnyArgs(typed)
		}
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, redactConfigValueIn("", parentKey, item))
		}
		return out
	default:
		return value
	}
}

func isServerCollectionKey(key string) bool {
	return strings.EqualFold(key, "servers") || strings.EqualFold(key, "mcp_servers")
}

func isSecretKey(key string) bool {
	upper := strings.ToUpper(key)
	return strings.Contains(upper, "TOKEN") ||
		strings.Contains(upper, "SECRET") ||
		strings.Contains(upper, "KEY") ||
		strings.Contains(upper, "PASSWORD")
}

func maskAnyArgs(value any) any {
	args, err := stringSlice(value)
	if err != nil {
		return value
	}
	masked := maskArgs(args)
	out := make([]any, 0, len(masked))
	for _, arg := range masked {
		out = append(out, arg)
	}
	return out
}

func maskAnyEnv(value any) any {
	env, err := stringMap(value)
	if err != nil {
		return value
	}
	masked := maskEnv(env)
	out := make(map[string]any, len(masked))
	for key, value := range masked {
		out[key] = value
	}
	return out
}

func maskEnv(env map[string]string) map[string]string {
	if len(env) == 0 {
		return env
	}
	out := make(map[string]string, len(env))
	for key, value := range env {
		out[key] = maskSecret(key, value)
	}
	return out
}

func configData(path string, incoming map[string]config.Server, overwrite bool) ([]byte, []byte, bool, error) {
	before, readErr := os.ReadFile(path)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return nil, nil, false, readErr
	}
	cfg, err := readLazyConfig(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, nil, false, err
		}
		cfg = &config.Config{Servers: map[string]config.Server{}}
	}
	changed := removeLazyConfigServers(cfg, path)
	if len(incoming) == 0 && len(cfg.Servers) == 0 {
		return before, before, false, nil
	}
	for name, srv := range incoming {
		existing, exists := cfg.Servers[name]
		if exists && !overwrite && sameImportedServer(name, existing, srv) {
			continue
		}
		if exists && !overwrite {
			return nil, nil, false, fmt.Errorf("server %q already exists", name)
		}
		cfg.Servers[name] = srv
		changed = true
	}
	if !changed {
		return before, before, false, nil
	}
	applyDefaults(cfg)
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, nil, false, err
	}
	return before, data, true, nil
}

func applyDefaults(cfg *config.Config) {
	for name, srv := range cfg.Servers {
		if srv.IdleTimeout == 0 {
			srv.IdleTimeout = config.Duration(5 * time.Minute)
		}
		if srv.RequestTimeout == 0 {
			srv.RequestTimeout = config.Duration(10 * time.Minute)
		}
		cfg.Servers[name] = srv
	}
}

func removeLazyConfigServers(cfg *config.Config, configPath string) bool {
	changed := false
	for name, srv := range cfg.Servers {
		if isLazyConfigServer(srv, configPath) {
			delete(cfg.Servers, name)
			changed = true
		}
	}
	return changed
}

func sameImportedServer(name string, existing, incoming config.Server) bool {
	existing = normalizedServer(existing)
	incoming = normalizedServer(incoming)
	return existing.Command == incoming.Command &&
		stringSlicesEqual(existing.Args, incoming.Args) &&
		stringMapsEqual(existing.Env, incoming.Env) &&
		existing.NamespaceOrName(name) == incoming.NamespaceOrName(name) &&
		existing.IdleTimeout == incoming.IdleTimeout &&
		existing.RequestTimeout == incoming.RequestTimeout &&
		(len(incoming.Tools) == 0 || toolsEqual(existing.Tools, incoming.Tools))
}

func normalizedServer(srv config.Server) config.Server {
	if srv.IdleTimeout == 0 {
		srv.IdleTimeout = config.Duration(5 * time.Minute)
	}
	if srv.RequestTimeout == 0 {
		srv.RequestTimeout = config.Duration(10 * time.Minute)
	}
	return srv
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func stringMapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, aValue := range a {
		if b[key] != aValue {
			return false
		}
	}
	return true
}

func toolsEqual(a, b []config.Tool) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || a[i].Description != b[i].Description {
			return false
		}
	}
	return true
}

func FormatPlan(plan *Plan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "source: %s\n", plan.Source)
	fmt.Fprintf(&b, "target config: %s\n", plan.ConfigPath)
	if len(plan.SourceFiles) > 0 {
		fmt.Fprintf(&b, "source files:\n")
		for _, path := range plan.SourceFiles {
			fmt.Fprintf(&b, "  - %s\n", path)
		}
	}
	fmt.Fprintf(&b, "servers to import:\n")
	for _, name := range sortedServerNames(plan.Servers) {
		srv := plan.Servers[name]
		fmt.Fprintf(&b, "  - %s: %s\n", name, maskedCommandLine(srv))
		if len(srv.Env) > 0 {
			fmt.Fprintf(&b, "    env:\n")
			for _, key := range sortedEnvKeys(srv.Env) {
				fmt.Fprintf(&b, "      %s: %s\n", key, maskSecret(key, srv.Env[key]))
			}
		}
	}
	if len(plan.Skipped) > 0 {
		fmt.Fprintf(&b, "skipped:\n")
		for _, item := range plan.Skipped {
			fmt.Fprintf(&b, "  - %s\n", item)
		}
	}
	if len(plan.Blocked) > 0 {
		fmt.Fprintf(&b, "blocking source client update:\n")
		for _, item := range plan.Blocked {
			fmt.Fprintf(&b, "  - %s\n", item)
		}
	}
	if len(plan.Conflicts) > 0 {
		fmt.Fprintf(&b, "conflicts:\n")
		for _, item := range plan.Conflicts {
			fmt.Fprintf(&b, "  - %s\n", item)
		}
	}
	if len(plan.ChangedFiles) > 0 {
		fmt.Fprintf(&b, "files changed:\n")
		for _, path := range plan.ChangedFiles {
			fmt.Fprintf(&b, "  - %s\n", path)
		}
	}
	if len(plan.Backups) > 0 {
		fmt.Fprintf(&b, "backups created:\n")
		for _, path := range plan.Backups {
			fmt.Fprintf(&b, "  - %s\n", path)
		}
	}
	if len(plan.Diffs) > 0 {
		fmt.Fprintf(&b, "diffs:\n")
		for _, diff := range plan.Diffs {
			fmt.Fprintf(&b, "%s", diff.Diff)
		}
	}
	return b.String()
}

func UnifiedDiff(oldName, newName string, oldData, newData []byte) string {
	oldLines := splitDiffLines(string(oldData))
	newLines := splitDiffLines(string(newData))
	ops := diffLines(oldLines, newLines)
	var b strings.Builder
	fmt.Fprintf(&b, "--- %s\n", oldName)
	fmt.Fprintf(&b, "+++ %s\n", newName)
	fmt.Fprintf(&b, "@@ -1,%d +1,%d @@\n", len(oldLines), len(newLines))
	for _, op := range ops {
		fmt.Fprintf(&b, "%c%s", op.kind, op.line)
		if !strings.HasSuffix(op.line, "\n") {
			fmt.Fprintf(&b, "\n")
		}
	}
	return b.String()
}

type diffOp struct {
	kind rune
	line string
}

func splitDiffLines(value string) []string {
	if value == "" {
		return nil
	}
	lines := strings.SplitAfter(value, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func diffLines(oldLines, newLines []string) []diffOp {
	lcs := make([][]int, len(oldLines)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(newLines)+1)
	}
	for i := len(oldLines) - 1; i >= 0; i-- {
		for j := len(newLines) - 1; j >= 0; j-- {
			if oldLines[i] == newLines[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	var ops []diffOp
	for i, j := 0, 0; i < len(oldLines) || j < len(newLines); {
		switch {
		case i < len(oldLines) && j < len(newLines) && oldLines[i] == newLines[j]:
			ops = append(ops, diffOp{kind: ' ', line: oldLines[i]})
			i++
			j++
		case j < len(newLines) && (i == len(oldLines) || lcs[i][j+1] > lcs[i+1][j]):
			ops = append(ops, diffOp{kind: '+', line: newLines[j]})
			j++
		default:
			ops = append(ops, diffOp{kind: '-', line: oldLines[i]})
			i++
		}
	}
	return ops
}

func readLazyConfig(path string) (*config.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg config.Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.Servers == nil {
		cfg.Servers = map[string]config.Server{}
	}
	return &cfg, nil
}

func validateLazyConfigFile(path string, allowEmpty bool) error {
	cfg, err := readLazyConfig(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if len(cfg.Servers) == 0 && allowEmpty {
		return nil
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("validate %s: %w", path, err)
	}
	return nil
}

func validateExistingLazyConfigFile(path string) error {
	return validateLazyConfigFile(path, true)
}

func validateWrittenLazyConfigFile(path string) error {
	return validateLazyConfigFile(path, false)
}

func readCodexConfig(path string) (*clientConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	serversValue, ok := raw["mcp_servers"]
	if !ok {
		return &clientConfig{MCPServers: map[string]clientServer{}}, nil
	}
	serversMap, ok := serversValue.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("validate %s: [mcp_servers] must be a table", path)
	}
	out := &clientConfig{MCPServers: map[string]clientServer{}}
	for name, value := range serversMap {
		if name == "" {
			return nil, fmt.Errorf("validate %s: mcp server name must not be empty", path)
		}
		table, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("validate %s: [mcp_servers.%s] must be a table", path, name)
		}
		srv, err := parseCodexServer(path, name, table)
		if err != nil {
			return nil, err
		}
		out.MCPServers[name] = srv
	}
	return out, nil
}

func parseCodexServer(path, name string, table map[string]any) (clientServer, error) {
	var srv clientServer
	if serverType, ok := table["type"]; ok {
		serverTypeString, ok := serverType.(string)
		if !ok {
			return srv, fmt.Errorf("validate %s: [mcp_servers.%s].type must be a string", path, name)
		}
		srv.Type = serverTypeString
	}
	if url, ok := table["url"]; ok {
		urlString, ok := url.(string)
		if !ok {
			return srv, fmt.Errorf("validate %s: [mcp_servers.%s].url must be a string", path, name)
		}
		srv.URL = urlString
	}
	command, ok := table["command"]
	if !ok {
		if srv.Type != "" || srv.URL != "" {
			return srv, nil
		}
		return srv, fmt.Errorf("validate %s: [mcp_servers.%s].command is required", path, name)
	}
	commandString, ok := command.(string)
	if !ok || commandString == "" {
		return srv, fmt.Errorf("validate %s: [mcp_servers.%s].command must be a non-empty string", path, name)
	}
	srv.Command = commandString
	if args, ok := table["args"]; ok {
		parsed, err := stringSlice(args)
		if err != nil {
			return srv, fmt.Errorf("validate %s: [mcp_servers.%s].args %w", path, name, err)
		}
		srv.Args = parsed
	}
	if env, ok := table["env"]; ok {
		parsed, err := stringMap(env)
		if err != nil {
			return srv, fmt.Errorf("validate %s: [mcp_servers.%s].env %w", path, name, err)
		}
		srv.Env = parsed
	}
	return srv, nil
}

func stringSlice(value any) ([]string, error) {
	values, ok := value.([]any)
	if !ok {
		return nil, errors.New("must be an array of strings")
	}
	out := make([]string, 0, len(values))
	for _, item := range values {
		s, ok := item.(string)
		if !ok {
			return nil, errors.New("must be an array of strings")
		}
		out = append(out, s)
	}
	return out, nil
}

func stringMap(value any) (map[string]string, error) {
	values, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("must be a table of string values")
	}
	out := make(map[string]string, len(values))
	for key, item := range values {
		s, ok := item.(string)
		if !ok {
			return nil, errors.New("must be a table of string values")
		}
		out[key] = s
	}
	return out, nil
}

func backupFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	stamp := nowUTC().Format("20060102T150405.000000000Z")
	for attempt := 0; attempt < 100; attempt++ {
		backup := fmt.Sprintf("%s.bak.%s", path, stamp)
		if attempt > 0 {
			backup = fmt.Sprintf("%s.%d", backup, attempt)
		}
		if err := writeNewFile(backup, data); err != nil {
			if errors.Is(err, os.ErrExist) {
				continue
			}
			return "", err
		}
		return backup, nil
	}
	return "", fmt.Errorf("create backup for %s: too many name collisions", path)
}

func writeNewFile(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	n, err := file.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

func writeConfigFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	n, err := tmp.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	cleanup = false
	return os.Chmod(path, 0o600)
}

func copyEnv(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func sortedServerNames(servers map[string]config.Server) []string {
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sortedEnvKeys(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func maskedCommandLine(srv config.Server) string {
	return strings.Join(append([]string{srv.Command}, maskArgs(srv.Args)...), " ")
}

func maskSecret(key, value string) string {
	upper := strings.ToUpper(key)
	if strings.Contains(upper, "TOKEN") || strings.Contains(upper, "SECRET") || strings.Contains(upper, "KEY") || strings.Contains(upper, "PASSWORD") {
		return "<redacted>"
	}
	if value == "" {
		return ""
	}
	return "<set>"
}

func maskArgs(args []string) []string {
	out := append([]string(nil), args...)
	for i, arg := range out {
		if i > 0 && !strings.Contains(out[i-1], "=") && isSecretFlag(out[i-1]) {
			out[i] = "<redacted>"
			continue
		}
		if secretAssignment(arg) {
			key, _, ok := strings.Cut(arg, "=")
			if ok {
				out[i] = key + "=<redacted>"
			}
		}
	}
	return out
}

func isSecretFlag(arg string) bool {
	key := strings.ReplaceAll(strings.TrimLeft(arg, "-"), "-", "_")
	return isSecretKey(key)
}

func secretAssignment(arg string) bool {
	key, _, ok := strings.Cut(arg, "=")
	if !ok {
		return false
	}
	return isSecretFlag(key)
}

func isLazyMCPProxy(srv clientServer) bool {
	if filepath.Base(srv.Command) != "lazymcp" {
		return false
	}
	for _, arg := range srv.Args {
		if arg == "serve" {
			return true
		}
	}
	return false
}

func lazyProxyConfigPath(args []string) (string, bool) {
	for i, arg := range args {
		if arg == "--config" || arg == "-c" {
			if i+1 < len(args) {
				return args[i+1], true
			}
			return "", false
		}
		if value, ok := strings.CutPrefix(arg, "--config="); ok {
			return value, true
		}
	}
	return "", false
}

func samePath(a, b string) bool {
	return canonicalPath(a) == canonicalPath(b)
}

func canonicalPath(path string) string {
	if path == "" {
		return ""
	}
	if !filepath.IsAbs(path) {
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
	}
	path = filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

func isLazyConfigServer(srv config.Server, configPath string) bool {
	client := clientServer{Command: srv.Command, Args: srv.Args}
	if !isLazyMCPProxy(client) {
		return false
	}
	proxyConfigPath, ok := lazyProxyConfigPath(srv.Args)
	if !ok {
		return samePath(configPath, config.DefaultPath())
	}
	return samePath(proxyConfigPath, configPath)
}
