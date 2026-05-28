package migrate

import (
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
	Source     Source
	ConfigPath string
	SourcePath string
	Write      bool
	Overwrite  bool
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
	MCPServers map[string]clientServer
}

type clientServer struct {
	Command string
	Args    []string
	Env     map[string]string
}

func Run(opts Options) (*Plan, error) {
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
	raw, err := readCodexConfig(path)
	if err != nil {
		return nil, nil, nil, err
	}
	servers, skipped := convert(raw.MCPServers)
	if len(servers) == 0 {
		return nil, nil, skipped, errors.New("no Codex MCP servers to import")
	}
	return servers, []string{path}, skipped, nil
}

func convert(servers map[string]clientServer) (map[string]config.Server, []string) {
	out := map[string]config.Server{}
	var skipped []string
	for name, srv := range servers {
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
	if err := writeConfigFile(path, data); err != nil {
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
		return nil, fmt.Errorf("validate %s: missing [mcp_servers] table", path)
	}
	serversMap, ok := serversValue.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("validate %s: [mcp_servers] must be a table", path)
	}
	if len(serversMap) == 0 {
		return nil, fmt.Errorf("validate %s: [mcp_servers] must define at least one server table", path)
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
	command, ok := table["command"]
	if !ok {
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
	backup := fmt.Sprintf("%s.bak.%s", path, time.Now().UTC().Format("20060102T150405.000000000Z"))
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if err := writeNewFile(backup, data); err != nil {
		return "", err
	}
	return backup, nil
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
	upper := strings.ToUpper(strings.TrimLeft(arg, "-"))
	return strings.Contains(upper, "TOKEN") || strings.Contains(upper, "SECRET") || strings.Contains(upper, "PASSWORD") || strings.Contains(upper, "API-KEY") || strings.Contains(upper, "API_KEY")
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
