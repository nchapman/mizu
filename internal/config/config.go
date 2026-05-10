package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Site   Site   `yaml:"site"`
	Server Server `yaml:"server"`
	Theme  Theme  `yaml:"theme"`
	Paths  Paths  `yaml:"paths"`
	Poller Poller `yaml:"poller"`
	Limits Limits `yaml:"limits"`
}

// Theme selects the active public-site theme and supplies overrides
// for any settings the theme exposes. Themes live at ./themes/<name>/;
// the "default" theme is embedded in the binary so an empty Theme
// block just renders the default look.
type Theme struct {
	Name     string         `yaml:"name"`
	Settings map[string]any `yaml:"settings"`
}

type Site struct {
	Title       string `yaml:"title"`
	Author      string `yaml:"author"`
	BaseURL     string `yaml:"base_url"`
	Description string `yaml:"description"`
}

type Server struct {
	Addr string `yaml:"addr"`
	TLS  TLS    `yaml:"tls"`
}

// TLS controls automatic HTTPS via CertMagic. When Enabled is false the
// binary serves plain HTTP on Server.Addr (the legacy behavior). When
// enabled, the binary binds Addr (HTTPS) and HTTPAddr (ACME challenge +
// redirect) and obtains certificates from Let's Encrypt for Domains.
//
// Email is required by the ACME terms of service. Staging swaps in the
// Let's Encrypt staging endpoint, which has higher rate limits and
// issues certs that browsers don't trust — useful for end-to-end
// testing without burning the production rate-limit budget.
type TLS struct {
	Enabled  bool     `yaml:"enabled"`
	Domains  []string `yaml:"domains"`
	Email    string   `yaml:"email"`
	Addr     string   `yaml:"addr"`
	HTTPAddr string   `yaml:"http_addr"`
	Staging  bool     `yaml:"staging"`
}

type Paths struct {
	Content       string `yaml:"content"`
	Media         string `yaml:"media"`
	Cache         string `yaml:"cache"`
	State         string `yaml:"state"`
	Certs         string `yaml:"certs"`
	AdminDist     string `yaml:"admin_dist"`
	Subscriptions string `yaml:"subscriptions"`
}

type Poller struct {
	Interval  time.Duration `yaml:"interval"`
	UserAgent string        `yaml:"user_agent"`
}

// Limits collects every knob a deployment might tune to defend itself
// from sloppy clients and abuse. The defaults are deliberately
// conservative for a single-user appliance; bump them only if you hit
// a wall.
type Limits struct {
	ReadHeaderTimeout time.Duration `yaml:"read_header_timeout"`
	ReadTimeout       time.Duration `yaml:"read_timeout"`
	WriteTimeout      time.Duration `yaml:"write_timeout"`
	IdleTimeout       time.Duration `yaml:"idle_timeout"`
	MaxHeaderBytes    int           `yaml:"max_header_bytes"`

	Body BodyLimits `yaml:"body"`
	Rate RateLimits `yaml:"rate"`
}

// BodyLimits cap the request body size accepted by each endpoint that
// reads one. Media uploads have their own much larger cap inside the
// media package because that's the one place we expect big payloads.
type BodyLimits struct {
	Login      int64 `yaml:"login"`
	Setup      int64 `yaml:"setup"`
	Post       int64 `yaml:"post"`
	Webmention int64 `yaml:"webmention"`
}

// RateLimits define per-IP request budgets for the abuse-sensitive
// endpoints. Global is the safety-net cap that applies to every
// request before route-specific limits.
type RateLimits struct {
	Login      RateSpec `yaml:"login"`
	Setup      RateSpec `yaml:"setup"`
	Webmention RateSpec `yaml:"webmention"`
	Global     RateSpec `yaml:"global"`
}

type RateSpec struct {
	Requests int           `yaml:"requests"`
	Per      time.Duration `yaml:"per"`
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
	c.ApplyDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	for _, dir := range []string{
		filepath.Join(c.Paths.Content, "posts"),
		filepath.Join(c.Paths.Content, "drafts"),
		c.Paths.Media,
		c.Paths.Cache,
		c.Paths.State,
		c.Paths.Certs,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	return &c, nil
}

// ApplyDefaults fills in any unset fields with sensible production
// defaults. Load() calls this automatically; tests that build a
// Config in-memory should call it as well so the resulting struct
// behaves like one parsed from YAML.
func (c *Config) ApplyDefaults() {
	if c.Poller.Interval == 0 {
		c.Poller.Interval = time.Hour
	}
	if c.Poller.UserAgent == "" {
		c.Poller.UserAgent = "mizu/0.1"
	}
	if c.Paths.Subscriptions == "" {
		c.Paths.Subscriptions = "./subscriptions.opml"
	}
	if c.Paths.Certs == "" {
		c.Paths.Certs = filepath.Join(c.Paths.State, "certs")
	}
	if c.Server.TLS.Addr == "" {
		c.Server.TLS.Addr = ":443"
	}
	if c.Server.TLS.HTTPAddr == "" {
		c.Server.TLS.HTTPAddr = ":80"
	}
	if c.Theme.Name == "" {
		c.Theme.Name = "default"
	}

	l := &c.Limits
	if l.ReadHeaderTimeout == 0 {
		l.ReadHeaderTimeout = 10 * time.Second
	}
	if l.ReadTimeout == 0 {
		l.ReadTimeout = 30 * time.Second
	}
	if l.WriteTimeout == 0 {
		l.WriteTimeout = 60 * time.Second
	}
	if l.IdleTimeout == 0 {
		l.IdleTimeout = 120 * time.Second
	}
	if l.MaxHeaderBytes == 0 {
		l.MaxHeaderBytes = 1 << 20 // 1 MiB
	}
	if l.Body.Login == 0 {
		l.Body.Login = 1 << 10 // 1 KiB
	}
	if l.Body.Setup == 0 {
		l.Body.Setup = 1 << 10
	}
	if l.Body.Post == 0 {
		l.Body.Post = 256 << 10 // 256 KiB
	}
	if l.Body.Webmention == 0 {
		l.Body.Webmention = 4 << 10
	}
	defaultRate(&l.Rate.Login, 10, time.Minute)
	defaultRate(&l.Rate.Setup, 5, time.Hour)
	defaultRate(&l.Rate.Webmention, 30, time.Minute)
	defaultRate(&l.Rate.Global, 600, time.Minute)
}

func defaultRate(r *RateSpec, requests int, per time.Duration) {
	if r.Requests == 0 {
		r.Requests = requests
	}
	if r.Per == 0 {
		r.Per = per
	}
}

func (c *Config) validate() error {
	if c.Server.TLS.Enabled {
		if len(c.Server.TLS.Domains) == 0 {
			return fmt.Errorf("server.tls.enabled requires at least one entry in server.tls.domains")
		}
		if c.Server.TLS.Email == "" {
			return fmt.Errorf("server.tls.enabled requires server.tls.email (ACME contact)")
		}
	}
	return nil
}
