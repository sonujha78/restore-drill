package config

import (
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Keys containing one of these words name a secret. They are only allowed when
// the key ends in _env, because then the value is the NAME of an environment variable.
var secretKeyWords = []string{"password", "passwd", "pwd", "secret", "token", "dsn", "connection_string", "conn_string", "api_key", "apikey"}

// Values that look like a credential: user:pass@ in a URL, or password= in a string.
var credentialValue = []*regexp.Regexp{
	regexp.MustCompile(`(?i)[a-z][a-z0-9+.\-]*://[^/\s:@]+:[^/\s@]+@`),
	regexp.MustCompile(`(?i)\b(password|passwd|pwd)\s*=`),
}

func keyLooksSecret(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	if strings.HasSuffix(k, "_env") {
		return false
	}
	for _, w := range secretKeyWords {
		if strings.Contains(k, w) {
			return true
		}
	}
	return false
}

// scanSecrets walks the whole YAML tree (any depth) and reports secret-looking
// keys and credential-looking values. It never puts a value into a message.
func scanSecrets(root *yaml.Node) []string {
	var probs []string
	var walk func(n *yaml.Node, key string)
	walk = func(n *yaml.Node, key string) {
		switch n.Kind {
		case yaml.DocumentNode, yaml.SequenceNode:
			for _, c := range n.Content {
				walk(c, key)
			}
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				k, v := n.Content[i], n.Content[i+1]
				if k.Kind == yaml.ScalarNode && keyLooksSecret(k.Value) {
					probs = append(probs, fmt.Sprintf(
						"line %d: key %q looks like a secret; write the NAME of an environment variable under a key ending in _env instead",
						k.Line, k.Value))
					continue
				}
				walk(v, k.Value)
			}
		case yaml.ScalarNode:
			for _, re := range credentialValue {
				if re.MatchString(n.Value) {
					probs = append(probs, fmt.Sprintf(
						"line %d: value of %q looks like a credential or connection string (value not shown); use an environment variable name instead",
						n.Line, key))
					break
				}
			}
		}
	}
	walk(root, "list item")
	return probs
}
