package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Servers map[string]Server `yaml:"servers"`
}

type Server struct {
	Command        string   `yaml:"command"`
	Args           []string `yaml:"args"`
	Namespace      string   `yaml:"namespace"`
	IdleTimeout    Duration `yaml:"idle_timeout"`
	RequestTimeout Duration `yaml:"request_timeout"`
	Tools          []Tool   `yaml:"tools"`
}

type Tool struct {
	Name        string         `json:"name" yaml:"name"`
	Description string         `json:"description,omitempty" yaml:"description"`
	InputSchema map[string]any `json:"inputSchema" yaml:"input_schema"`
}

func DefaultPath() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "lazymcp", "config.yaml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "config.yaml"
	}
	return filepath.Join(home, ".config", "lazymcp", "config.yaml")
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if len(cfg.Servers) == 0 {
		return nil, fmt.Errorf("config must define at least one server")
	}

	namespaces := map[string]string{}
	toolNames := map[string]string{}
	for name, srv := range cfg.Servers {
		if srv.Command == "" {
			return nil, fmt.Errorf("server %q command is required", name)
		}
		namespace := srv.NamespaceOrName(name)
		if existing := namespaces[namespace]; existing != "" {
			return nil, fmt.Errorf("namespace %q is used by both %q and %q", namespace, existing, name)
		}
		namespaces[namespace] = name
		if srv.IdleTimeout == 0 {
			srv.IdleTimeout = Duration(5 * time.Minute)
		}
		if srv.RequestTimeout == 0 {
			srv.RequestTimeout = Duration(10 * time.Minute)
		}
		for _, tool := range srv.Tools {
			exposedName := namespace + "." + strings.TrimPrefix(tool.Name, namespace+".")
			if existing := toolNames[exposedName]; existing != "" {
				return nil, fmt.Errorf("tool %q is exposed by both %q and %q", exposedName, existing, name)
			}
			toolNames[exposedName] = name
		}
		cfg.Servers[name] = srv
	}
	return &cfg, nil
}

func (c *Config) Tools() []Tool {
	var tools []Tool
	for name, srv := range c.Servers {
		namespace := srv.NamespaceOrName(name)
		for _, tool := range srv.Tools {
			out := tool
			out.Name = namespace + "." + strings.TrimPrefix(tool.Name, namespace+".")
			if out.InputSchema == nil {
				out.InputSchema = map[string]any{"type": "object"}
			}
			tools = append(tools, out)
		}
	}
	return tools
}

func (c *Config) ServerForTool(toolName string) (string, Server, string, bool) {
	for name, srv := range c.Servers {
		namespace := srv.NamespaceOrName(name)
		prefix := namespace + "."
		if strings.HasPrefix(toolName, prefix) {
			return name, srv, strings.TrimPrefix(toolName, prefix), true
		}
	}
	return "", Server{}, "", false
}

func (s Server) NamespaceOrName(name string) string {
	if s.Namespace != "" {
		return s.Namespace
	}
	return name
}

func (s Server) CommandLine() string {
	return strings.Join(append([]string{s.Command}, s.Args...), " ")
}
