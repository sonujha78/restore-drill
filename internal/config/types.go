// Package config loads and validates the targets YAML. The file never holds
// secrets: connection details come from environment variables and the file
// only names the variable (keys ending in _env).
package config

import (
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a Go duration written in YAML as 30m, 60s or 24h.
type Duration struct {
	time.Duration
}

// UnmarshalYAML parses a duration. The error never repeats the value, so a
// secret pasted into a duration field cannot leak through an error message.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: duration must be a single value such as 30m or 24h", n.Line)
	}
	v, err := time.ParseDuration(n.Value)
	if err != nil {
		return fmt.Errorf("line %d: invalid duration (use Go syntax such as 30m, 60s or 24h; days are not supported, write 24h)", n.Line)
	}
	d.Duration = v
	return nil
}

// Config is the whole targets file.
type Config struct {
	Targets []Target `yaml:"targets"`
}

// Target is one backup target.
type Target struct {
	Name      string            `yaml:"name"`
	Type      string            `yaml:"type"`
	Location  Location          `yaml:"location"`
	Manifest  string            `yaml:"manifest"`
	Frequency Duration          `yaml:"frequency"`
	Retention Retention         `yaml:"retention"`
	RPO       Duration          `yaml:"rpo"`
	RTO       Duration          `yaml:"rto"`
	MinBytes  int64             `yaml:"min_bytes"`
	Canary    *Canary           `yaml:"canary"`
	Structure Structure         `yaml:"structure"`
	Content   Content           `yaml:"content"`
	AppChecks []AppCheck        `yaml:"app_checks"`
	Options   map[string]string `yaml:"options"`
}

// Location says where the backup files are and which file names count.
type Location struct {
	Dir     string `yaml:"dir"`
	Pattern string `yaml:"pattern"`
}

// Retention is how many copies must exist.
type Retention struct {
	MinCopies int `yaml:"min_copies"`
	MaxCopies int `yaml:"max_copies"`
}

// Canary says where the system wrote its marker timestamp before the backup.
// Either Table+Column (databases) or Path (file trees). Nil means no canary.
type Canary struct {
	Table  string `yaml:"table"`
	Column string `yaml:"column"`
	Path   string `yaml:"path"`
}

// Structure is the expected schema or file tree after restore.
type Structure struct {
	Tables []TableSpec `yaml:"tables"`
	Paths  []string    `yaml:"paths"`
}

// TableSpec is one expected table and its columns.
type TableSpec struct {
	Name    string   `yaml:"name"`
	Columns []string `yaml:"columns"`
}

// Content holds the content-level expectations (counts only, never row data).
type Content struct {
	RowCounts []RowCount `yaml:"row_counts"`
}

// RowCount is the allowed row count range of one table.
type RowCount struct {
	Table string `yaml:"table"`
	Min   int64  `yaml:"min"`
	Max   int64  `yaml:"max"`
}

// AppCheck is a custom query or command that must return the expected result.
type AppCheck struct {
	Name    string `yaml:"name"`
	Query   string `yaml:"query"`
	Command string `yaml:"command"`
	Expect  string `yaml:"expect"`
}
