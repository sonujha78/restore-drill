package verify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/sonujha78/restore-drill/internal/config"
)

// allowedCommands never write anything. Commands run without a shell.
var allowedCommands = map[string]bool{
	"grep": true, "wc": true, "cat": true, "ls": true, "head": true, "sha256sum": true, "stat": true, "test": true,
}

// FileSource reads a restored file tree.
type FileSource struct {
	root string
}

// NewFileSource reads the tree below root (a sandbox directory).
func NewFileSource(root string) *FileSource { return &FileSource{root: root} }

func relOK(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, `\`) {
		return false
	}
	c := path.Clean(p)
	return c != "." && c != ".." && !strings.HasPrefix(c, "../")
}

func (f *FileSource) Tables() (map[string][]string, error)        { return nil, ErrUnsupported }
func (f *FileSource) RowCount(string) (int64, error)              { return 0, ErrUnsupported }
func (f *FileSource) SampleHash(string, int) (string, int, error) { return "", 0, ErrUnsupported }
func (f *FileSource) Scalar(string) (string, error)               { return "", ErrUnsupported }
func (f *FileSource) Integrity() (string, error)                  { return "", ErrUnsupported }

func (f *FileSource) PathExists(rel string) (bool, error) {
	if !relOK(rel) {
		return false, errors.New("path must be relative and stay inside the restored tree")
	}
	_, err := os.Lstat(filepath.Join(f.root, filepath.FromSlash(rel)))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func (f *FileSource) Canary(c config.Canary) (time.Time, error) {
	if c.Path == "" {
		return time.Time{}, ErrUnsupported
	}
	if !relOK(c.Path) {
		return time.Time{}, errors.New("canary path must be relative and stay inside the restored tree")
	}
	full := filepath.Join(f.root, filepath.FromSlash(c.Path))
	st, err := os.Lstat(full)
	if errors.Is(err, os.ErrNotExist) {
		return time.Time{}, ErrNoCanaryData
	}
	if err != nil {
		return time.Time{}, err
	}
	if !st.Mode().IsRegular() || st.Size() > 256 {
		return time.Time{}, errors.New("canary file is not a small regular file")
	}
	b, err := os.ReadFile(full)
	if err != nil {
		return time.Time{}, err
	}
	ts, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(b)))
	if err != nil {
		return time.Time{}, errors.New("canary file does not hold an RFC 3339 timestamp (content not shown)")
	}
	return ts, nil
}

// Command runs one allowed program inside the restored tree. A non-zero exit
// that still printed an answer (grep -c with no match) is an answer, not a failure.
func (f *FileSource) Command(line string) (string, error) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return "", errors.New("empty command")
	}
	if !allowedCommands[fields[0]] {
		return "", fmt.Errorf("program %q is not on the allowed list", fields[0])
	}
	for _, a := range fields[1:] {
		if strings.HasPrefix(a, "/") {
			return "", errors.New("absolute paths are refused")
		}
		for _, part := range strings.Split(a, "/") {
			if part == ".." {
				return "", errors.New("arguments that climb out of the tree are refused")
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, fields[0], fields[1:]...)
	cmd.Dir = f.root
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if ctx.Err() != nil {
		return "", errors.New("command timed out")
	}
	var ee *exec.ExitError
	if err != nil && !errors.As(err, &ee) {
		return "", errors.New("command could not be started")
	}
	s := out.String()
	if len(s) > 4096 {
		s = s[:4096]
	}
	return strings.TrimSpace(s), nil
}
