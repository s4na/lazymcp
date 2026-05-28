package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Servers map[string]Server `yaml:"servers"`
	routes  map[string]Route
}

type Route struct {
	ServerName  string
	Server      Server
	BackendTool string
}

type Server struct {
	Command        string            `yaml:"command"`
	Args           []string          `yaml:"args"`
	Env            map[string]string `yaml:"env,omitempty"`
	Namespace      string            `yaml:"namespace"`
	IdleTimeout    Duration          `yaml:"idle_timeout"`
	RequestTimeout Duration          `yaml:"request_timeout"`
	Tools          []Tool            `yaml:"tools"`
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
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) Validate() error {
	if len(c.Servers) == 0 {
		return fmt.Errorf("config must define at least one server")
	}

	if err := c.rebuildRoutes(); err != nil {
		return err
	}
	return nil
}

func (c *Config) rebuildRoutes() error {
	namespaces := map[string]string{}
	toolNames := map[string]string{}
	routes := map[string]Route{}
	for name, srv := range c.Servers {
		if srv.Command == "" {
			return fmt.Errorf("server %q command is required", name)
		}
		namespace := srv.NamespaceOrName(name)
		if existing := namespaces[namespace]; existing != "" {
			return fmt.Errorf("namespace %q is used by both %q and %q", namespace, existing, name)
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
				return fmt.Errorf("tool %q is exposed by both %q and %q", exposedName, existing, name)
			}
			toolNames[exposedName] = name
			routes[exposedName] = Route{ServerName: name, Server: srv, BackendTool: strings.TrimPrefix(tool.Name, namespace+".")}
		}
		c.Servers[name] = srv
	}
	c.routes = routes
	return nil
}

func (c *Config) Tools() []Tool {
	var tools []Tool
	for _, name := range c.ServerNames() {
		srv := c.Servers[name]
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
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	return tools
}

func (c *Config) ServerForTool(toolName string) (string, Server, string, bool) {
	if c.routes != nil {
		route, ok := c.routes[toolName]
		return route.ServerName, route.Server, route.BackendTool, ok
	}
	return "", Server{}, "", false
}

func (c *Config) SetServerTools(name string, tools []Tool) error {
	srv, ok := c.Servers[name]
	if !ok {
		return fmt.Errorf("unknown server %q", name)
	}
	previous := srv.Tools
	srv.Tools = append([]Tool(nil), tools...)
	c.Servers[name] = srv
	if err := c.rebuildRoutes(); err != nil {
		srv.Tools = previous
		c.Servers[name] = srv
		_ = c.rebuildRoutes()
		return err
	}
	return nil
}

func (c *Config) ServerNames() []string {
	names := make([]string, 0, len(c.Servers))
	for name := range c.Servers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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
