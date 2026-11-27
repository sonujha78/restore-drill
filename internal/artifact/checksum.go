package artifact

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var sprintf = fmt.Sprintf

var hexRe = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// SHA256File returns the lower-case hex SHA-256 of a file.
func SHA256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ParseManifest reads sha256sum format: "<hash>  <name>" or "<hash> *<name>".
// Error messages carry line numbers only, never the line.
func ParseManifest(r io.Reader) (map[string]string, error) {
	m := map[string]string{}
	sc := bufio.NewScanner(r)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.IndexByte(line, ' ')
		if i < 0 {
			return nil, fmt.Errorf("manifest line %d: expected \"<sha256>  <file name>\"", n)
		}
		hash := line[:i]
		name := strings.TrimPrefix(strings.TrimLeft(line[i:], " "), "*")
		if !hexRe.MatchString(hash) {
			return nil, fmt.Errorf("manifest line %d: first field is not a sha256 hex digest", n)
		}
		if name == "" {
			return nil, fmt.Errorf("manifest line %d: file name is missing", n)
		}
		hash = strings.ToLower(hash)
		if old, ok := m[name]; ok && old != hash {
			return nil, fmt.Errorf("manifest line %d: %s is listed twice with different hashes", n, name)
		}
		m[name] = hash
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return m, nil
}

// CheckChecksum compares the file's SHA-256 with the manifest. Anything that
// stops the comparison from happening is unverified, never pass.
func CheckChecksum(path, manifestPath string) Result {
	const name = "checksum"
	unv := func(d string) Result { return Result{Check: name, Status: Unverified, Detail: d} }
	if manifestPath == "" {
		return unv("no manifest configured, integrity against a known hash cannot be checked")
	}
	mf, err := os.Open(manifestPath)
	if errors.Is(err, fs.ErrNotExist) {
		return unv("manifest file not found")
	}
	if err != nil {
		return unv("cannot open manifest: " + err.Error())
	}
	defer mf.Close()
	m, err := ParseManifest(mf)
	if err != nil {
		return unv(err.Error())
	}
	want, ok := m[filepath.Base(path)]
	if !ok {
		return unv("file is not listed in the manifest")
	}
	got, err := SHA256File(path)
	if err != nil {
		return unv("cannot read the file to hash it: " + err.Error())
	}
	if got != want {
		return Result{Check: name, Status: Fail,
			Detail: fmt.Sprintf("sha256 differs from manifest (manifest %s..., file %s...)", want[:12], got[:12])}
	}
	return Result{Check: name, Status: Pass, Detail: "sha256 matches manifest (" + got[:12] + "...)"}
}
