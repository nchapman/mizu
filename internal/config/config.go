package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Site   Site   `yaml:"site"`
	Server Server `yaml:"server"`
	Paths  Paths  `yaml:"paths"`
}

type Site struct {
	Title       string `yaml:"title"`
	Author      string `yaml:"author"`
	BaseURL     string `yaml:"base_url"`
	Description string `yaml:"description"`
}

type Server struct {
	Addr string `yaml:"addr"`
}

type Paths struct {
	Content   string `yaml:"content"`
	Media     string `yaml:"media"`
	Cache     string `yaml:"cache"`
	State     string `yaml:"state"`
	AdminDist string `yaml:"admin_dist"`
	Templates string `yaml:"templates"`
}

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	for _, dir := range []string{
		filepath.Join(c.Paths.Content, "posts"),
		filepath.Join(c.Paths.Content, "drafts"),
		c.Paths.Media,
		c.Paths.Cache,
		c.Paths.State,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	return &c, nil
}
