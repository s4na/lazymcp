package migrate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
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
	SourceCodex  Source = "codex"
	SourceClaude Source = "claude"
)

type Options struct {
	Source        Source
	ConfigPath    string
	SourcePath    string
	ProjectPath   string
	Write         bool
	Overwrite     bool
	DisableSource bool
}

type Plan struct {
	Source       Source
	ConfigPath   string
	SourceFiles  []string
	Servers      map[string]config.Server
	Conflicts    []string
	Skipped      []string
	ChangedFiles []string
	Backups      []string
}

type clientConfig struct {
	MCPServers map[string]clientServer `json:"mcpServers" toml:"mcp_servers"`
}

type claudeConfig struct {
	MCPServers map[string]clientServer `json:"mcpServers"`
	Projects   map[string]clientConfig `json:"projects"`
}

type clientServer struct {
	Command string            `json:"command" toml:"command"`
	Args    []string          `json:"args,omitempty" toml:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty" toml:"env,omitempty"`
}

func Run(opts Options) (*Plan, error) {
	if opts.DisableSource {
		return nil, errors.New("--disable-source is not supported yet; migrate with --write, then update the source client manually")
	}
	if opts.ConfigPath == "" {
		opts.ConfigPath = config.DefaultPath()
	}
	servers, sourceFiles, skipped, err := discover(opts)
	if err != nil {
		return nil, err
	}
	plan := &Plan{
		Source:      opts.Source,
		ConfigPath:  opts.ConfigPath,
		SourceFiles: sourceFiles,
		Servers:     servers,
		Skipped:     skipped,
	}
	conflicts, err := mergeConflicts(opts.ConfigPath, servers, opts.Overwrite)
	if err != nil {
		return nil, err
	}
	plan.Conflicts = conflicts
	if len(conflicts) > 0 {
		return plan, fmt.Errorf("migration has conflicts: %s", strings.Join(conflicts, "; "))
	}
	if opts.Write {
		changed, backups, err := writeConfig(opts.ConfigPath, servers, opts.Overwrite)
		if err != nil {
			return plan, err
		}
		plan.ChangedFiles = changed
		plan.Backups = backups
	}
	return plan, nil
}

func discover(opts Options) (map[string]config.Server, []string, []string, error) {
	switch opts.Source {
	case SourceCodex:
		return discoverCodex(opts)
	case SourceClaude:
		return discoverClaude(opts)
	default:
		return nil, nil, nil, fmt.Errorf("unsupported source %q", opts.Source)
	}
}

func discoverCodex(opts Options) (map[string]config.Server, []string, []string, error) {
	path := opts.SourcePath
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, nil, nil, err
		}
		path = filepath.Join(home, ".codex", "config.toml")
	}
	var raw clientConfig
	if err := readTOML(path, &raw); err != nil {
		return nil, nil, nil, err
	}
	servers, skipped := convert(raw.MCPServers)
	return servers, []string{path}, skipped, nil
}

func discoverClaude(opts Options) (map[string]config.Server, []string, []string, error) {
	paths := []string{}
	if opts.SourcePath != "" {
		paths = append(paths, opts.SourcePath)
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, nil, nil, err
		}
		paths = append(paths,
			filepath.Join(home, ".claude.json"),
			filepath.Join(home, ".claude", "settings.json"),
		)
		if opts.ProjectPath != "" {
			paths = append(paths,
				filepath.Join(opts.ProjectPath, ".mcp.json"),
				filepath.Join(opts.ProjectPath, ".claude", "settings.json"),
			)
		}
	}
	out := map[string]config.Server{}
	var sourceFiles []string
	var skipped []string
	for _, path := range unique(paths) {
		raw, err := readClaudeConfig(path, opts.ProjectPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				skipped = append(skipped, fmt.Sprintf("%s: not found", path))
				continue
			}
			return nil, nil, nil, err
		}
		sourceFiles = append(sourceFiles, path)
		converted, convertedSkipped := convert(raw)
		skipped = append(skipped, convertedSkipped...)
		for name, srv := range converted {
			if _, exists := out[name]; exists {
				return nil, nil, nil, fmt.Errorf("server %q is defined by multiple Claude config files", name)
			}
			out[name] = srv
		}
	}
	if len(sourceFiles) == 0 {
		return nil, nil, skipped, errors.New("no Claude config files found")
	}
	return out, sourceFiles, skipped, nil
}

func convert(servers map[string]clientServer) (map[string]config.Server, []string) {
	out := map[string]config.Server{}
	var skipped []string
	for name, srv := range servers {
		if srv.Command == "" {
			skipped = append(skipped, fmt.Sprintf("%s: missing command", name))
			continue
		}
		if isLazyMCPProxy(srv) {
			skipped = append(skipped, fmt.Sprintf("%s: already points to lazymcp", name))
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
		namespaces[srv.NamespaceOrName(name)] = name
	}
	for name, srv := range incoming {
		if _, ok := existing.Servers[name]; ok && !overwrite {
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
	cfg, err := readLazyConfig(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, nil, err
		}
		cfg = &config.Config{Servers: map[string]config.Server{}}
	}
	for name, srv := range incoming {
		if _, exists := cfg.Servers[name]; exists && !overwrite {
			return nil, nil, fmt.Errorf("server %q already exists", name)
		}
		cfg.Servers[name] = srv
	}
	applyDefaults(cfg)
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, nil, err
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
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return nil, nil, err
	}
	return []string{path}, backups, nil
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
	fmt.Fprintf(&b, "imported servers:\n")
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
	return b.String()
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

func readTOML(path string, out any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := toml.Unmarshal(data, out); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

func readJSON(path string, out any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

func readClaudeConfig(path, projectPath string) (map[string]clientServer, error) {
	if filepath.Ext(path) == ".toml" {
		var raw clientConfig
		if err := readTOML(path, &raw); err != nil {
			return nil, err
		}
		return raw.MCPServers, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw claudeConfig
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&raw); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	out := map[string]clientServer{}
	for name, srv := range raw.MCPServers {
		out[name] = srv
	}
	if projectPath != "" && len(raw.Projects) > 0 {
		project, err := filepath.Abs(projectPath)
		if err != nil {
			return nil, err
		}
		if cfg, ok := raw.Projects[project]; ok {
			for name, srv := range cfg.MCPServers {
				out[name] = srv
			}
		}
	}
	return out, nil
}

func backupFile(path string) (string, error) {
	backup := fmt.Sprintf("%s.bak.%s", path, time.Now().UTC().Format("20060102T150405Z"))
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(backup, data, 0o600); err != nil {
		return "", err
	}
	return backup, nil
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

func unique(paths []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, path := range paths {
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		out = append(out, path)
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
		upper := strings.ToUpper(arg)
		if i > 0 && isSecretFlag(out[i-1]) {
			out[i] = "<redacted>"
			continue
		}
		if strings.Contains(upper, "TOKEN=") || strings.Contains(upper, "SECRET=") || strings.Contains(upper, "PASSWORD=") || strings.Contains(upper, "API_KEY=") {
			key, _, ok := strings.Cut(arg, "=")
			if ok {
				out[i] = key + "=<redacted>"
			}
		}
	}
	return out
}

func isSecretFlag(arg string) bool {
	upper := strings.ToUpper(strings.TrimLeft(arg, "-"))
	return strings.Contains(upper, "TOKEN") || strings.Contains(upper, "SECRET") || strings.Contains(upper, "PASSWORD") || strings.Contains(upper, "API-KEY") || strings.Contains(upper, "API_KEY")
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
