package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Options controls validation.
type Options struct {
	// KnownTypes is the list of registered adapter types. When it is empty the
	// type is not checked, so config never has to change when an adapter is added.
	KnownTypes []string
}

// ValidationError lists every problem found, in a stable order.
type ValidationError struct {
	Problems []string
}

func (e *ValidationError) Error() string {
	return "invalid config:\n  - " + strings.Join(e.Problems, "\n  - ")
}

var backticked = regexp.MustCompile("`[^`]*`")

// redact removes quoted values from a yaml.v3 error. yaml.v3 prints the
// offending value inside backticks, and that value could be a secret.
func redact(s string) string {
	return backticked.ReplaceAllString(s, "`<redacted>`")
}

// Parse reads, scans for secrets, decodes strictly and validates.
func Parse(data []byte, opt Options) (*Config, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, &ValidationError{Problems: []string{"config is empty"}}
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("config is not valid YAML: %s", redact(err.Error()))
	}
	if probs := scanSecrets(&root); len(probs) > 0 {
		return nil, &ValidationError{Problems: probs}
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, &ValidationError{Problems: []string{"config is empty"}}
		}
		return nil, fmt.Errorf("config does not match the schema: %s", redact(err.Error()))
	}
	if probs := validate(&cfg, opt); len(probs) > 0 {
		return nil, &ValidationError{Problems: probs}
	}
	return &cfg, nil
}

// Load reads a file and parses it.
func Load(path string, opt Options) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return Parse(data, opt)
}
