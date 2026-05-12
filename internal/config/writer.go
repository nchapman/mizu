package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// SiteSettings is the subset of config.yml the admin wizard owns. Each
// field is a wizard-step output: title/author/description from the
// "Site basics" step, BaseURL from the "Domain" step, ThemeName from
// the "Theme" step (or "default" if the operator skipped it).
type SiteSettings struct {
	Title       string
	Author      string
	BaseURL     string
	Description string
	ThemeName   string
}

// ACMESettings is the wizard's TLS-step output. Domains MUST be
// non-empty when Enabled is true. Persisted under server.tls.acme so
// the always-on HTTPS listener (the cfg.Server.TLS block) stays a
// separate concern.
type ACMESettings struct {
	Enabled bool
	Domains []string
	Email   string
	Staging bool
}

// WriteSite merges SiteSettings into the YAML file at path, preserving
// comments and field order of any existing config. If the file doesn't
// exist it is created with a minimal scaffold. Atomic: writes a sibling
// temp file, fsyncs, and renames.
func WriteSite(path string, s SiteSettings) error {
	return mutateYAML(path, func(root *yaml.Node) error {
		site := ensureMap(root, "site")
		setScalar(site, "title", s.Title)
		setScalar(site, "author", s.Author)
		setScalar(site, "base_url", s.BaseURL)
		setScalar(site, "description", s.Description)
		theme := ensureMap(root, "theme")
		name := s.ThemeName
		if name == "" {
			name = "default"
		}
		setScalar(theme, "name", name)
		return nil
	})
}

// WriteACME merges ACMESettings into config.yml under server.tls.acme.
// Enabling without domains+email is rejected here so the wizard's
// confirm step is the only path that flips the live runner.
func WriteACME(path string, s ACMESettings) error {
	if s.Enabled && len(s.Domains) == 0 {
		return fmt.Errorf("WriteACME: enabled requires at least one domain")
	}
	if s.Enabled && s.Email == "" {
		return fmt.Errorf("WriteACME: enabled requires an ACME contact email")
	}
	return mutateYAML(path, func(root *yaml.Node) error {
		server := ensureMap(root, "server")
		tls := ensureMap(server, "tls")
		acme := ensureMap(tls, "acme")
		setBool(acme, "enabled", s.Enabled)
		setSequenceStrings(acme, "domains", s.Domains)
		setScalar(acme, "email", s.Email)
		setBool(acme, "staging", s.Staging)
		return nil
	})
}

// mutateYAML reads path (or starts a fresh document), runs fn against
// the root mapping node, and atomically writes the result back. The
// yaml.Node round-trip preserves leading comments and key order on
// every key the caller didn't touch.
func mutateYAML(path string, fn func(root *yaml.Node) error) error {
	var doc yaml.Node
	if b, err := os.ReadFile(path); err == nil {
		if err := yaml.Unmarshal(b, &doc); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", path, err)
	}

	// Empty file or absent file: bootstrap a document with a single
	// mapping root and a one-line header comment so the operator can
	// tell at a glance that the file is wizard-managed (but still
	// hand-editable).
	if doc.Kind == 0 {
		doc.Kind = yaml.DocumentNode
		doc.HeadComment = "Written by the mizu setup wizard. Hand-editable; see config.example.yml for the full set of knobs."
		doc.Content = []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}
	}
	if len(doc.Content) == 0 {
		doc.Content = []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("config root is not a mapping (%v)", root.Kind)
	}
	if err := fn(root); err != nil {
		return err
	}

	out, err := yaml.Marshal(&doc)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return atomicWrite(path, out)
}

// ensureMap returns the mapping node at root[key], inserting an empty
// mapping if it doesn't exist. Caller mutates the returned node.
func ensureMap(parent *yaml.Node, key string) *yaml.Node {
	if v := lookup(parent, key); v != nil {
		if v.Kind != yaml.MappingNode {
			// Hostile config: someone set "site: oops" as a scalar. Replace.
			v.Kind = yaml.MappingNode
			v.Tag = "!!map"
			v.Value = ""
			v.Content = nil
		}
		return v
	}
	k := &yaml.Node{Kind: yaml.ScalarNode, Value: key, Tag: "!!str"}
	v := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	parent.Content = append(parent.Content, k, v)
	return v
}

func lookup(parent *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(parent.Content); i += 2 {
		if parent.Content[i].Value == key {
			return parent.Content[i+1]
		}
	}
	return nil
}

func setScalar(parent *yaml.Node, key, value string) {
	if v := lookup(parent, key); v != nil {
		v.Kind = yaml.ScalarNode
		v.Tag = "!!str"
		v.Value = value
		v.Content = nil
		return
	}
	parent.Content = append(parent.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: key, Tag: "!!str"},
		&yaml.Node{Kind: yaml.ScalarNode, Value: value, Tag: "!!str"},
	)
}

func setBool(parent *yaml.Node, key string, value bool) {
	val := "false"
	if value {
		val = "true"
	}
	if v := lookup(parent, key); v != nil {
		v.Kind = yaml.ScalarNode
		v.Tag = "!!bool"
		v.Value = val
		v.Content = nil
		return
	}
	parent.Content = append(parent.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: key, Tag: "!!str"},
		&yaml.Node{Kind: yaml.ScalarNode, Value: val, Tag: "!!bool"},
	)
}

func setSequenceStrings(parent *yaml.Node, key string, values []string) {
	items := make([]*yaml.Node, len(values))
	for i, v := range values {
		items[i] = &yaml.Node{Kind: yaml.ScalarNode, Value: v, Tag: "!!str"}
	}
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle, Content: items}
	if v := lookup(parent, key); v != nil {
		*v = *seq
		return
	}
	parent.Content = append(parent.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: key, Tag: "!!str"},
		seq,
	)
}

// atomicWrite writes data to path via temp file + fsync + rename so a
// crash mid-write can't leave a half-truncated config.yml behind.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".config-*.yml.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		// If we never rename (error path), nuke the temp.
		_ = os.Remove(tmpName)
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
