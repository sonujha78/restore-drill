// Package sandbox is the only place restore-drill may write restored data.
// A sandbox is a directory that carries a marker file naming itself. Every
// operation re-checks the marker on disk, so a directory that is not a sandbox
// (or a zero-value Sandbox) can never be restored into or removed.
package sandbox

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sonujha78/restore-drill/internal/clock"
)

const (
	// MarkerName is the file that makes a directory a sandbox.
	MarkerName = ".rdr-sandbox"
	// DirPrefix starts the name of every sandbox directory.
	DirPrefix = "rdr-sbx-"

	magic = "restore-drill sandbox v1"
)

var (
	// ErrNotSandbox means the directory is not a marked restore-drill sandbox.
	ErrNotSandbox = errors.New("not a restore-drill sandbox")
	// ErrEscape means a path would leave the sandbox, or touch something reserved.
	ErrEscape = errors.New("path escapes the sandbox")
)

// Sandbox is a handle to a verified sandbox directory. Its fields are private,
// so the only ways to get a usable one are Create and Open.
type Sandbox struct {
	root string
	name string
}

func markerText(name string) string { return magic + "\nid=" + name + "\n" }

func notSandbox(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrNotSandbox, fmt.Sprintf(format, a...))
}

// checkRoot verifies the three things that make a directory a sandbox: its name
// starts with DirPrefix, it holds a regular marker file (not a symlink), and
// the marker names this very directory. Any doubt means refuse.
func checkRoot(root string) (string, error) {
	st, err := os.Lstat(root)
	if err != nil {
		return "", notSandbox("cannot stat directory")
	}
	if !st.IsDir() {
		return "", notSandbox("not a directory")
	}
	name := filepath.Base(root)
	if !strings.HasPrefix(name, DirPrefix) {
		return "", notSandbox("directory name does not start with %s", DirPrefix)
	}
	mp := filepath.Join(root, MarkerName)
	ms, err := os.Lstat(mp)
	if err != nil {
		return "", notSandbox("marker file %s is missing", MarkerName)
	}
	if !ms.Mode().IsRegular() {
		return "", notSandbox("marker is not a regular file")
	}
	want := markerText(name)
	if ms.Size() != int64(len(want)) {
		return "", notSandbox("marker content does not match this directory")
	}
	b, err := os.ReadFile(mp)
	if err != nil || string(b) != want {
		return "", notSandbox("marker content does not match this directory")
	}
	return name, nil
}

func writeMarker(root, name string) error {
	f, err := os.OpenFile(filepath.Join(root, MarkerName), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(f, markerText(name)); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Create makes a new, uniquely named, private sandbox directory inside base.
// The time in the name comes from the injected clock.
func Create(base string, clk clock.Clock) (*Sandbox, error) {
	st, err := os.Stat(base)
	if err != nil {
		return nil, fmt.Errorf("sandbox base: %w", err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("sandbox base %q is not a directory", base)
	}
	abs, err := filepath.Abs(base)
	if err != nil {
		return nil, fmt.Errorf("sandbox base: %w", err)
	}
	realBase, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("sandbox base: %w", err)
	}
	stamp := clk.Now().UTC().Format("20060102T150405Z")
	for attempt := 0; attempt < 5; attempt++ {
		var rnd [4]byte
		if _, err := rand.Read(rnd[:]); err != nil {
			return nil, fmt.Errorf("random id: %w", err)
		}
		name := DirPrefix + stamp + "-" + hex.EncodeToString(rnd[:])
		root := filepath.Join(realBase, name)
		if err := os.Mkdir(root, 0o700); err != nil {
			if errors.Is(err, os.ErrExist) {
				continue
			}
			return nil, fmt.Errorf("create sandbox directory: %w", err)
		}
		if err := writeMarker(root, name); err != nil {
			_ = os.Remove(filepath.Join(root, MarkerName))
			_ = os.Remove(root)
			return nil, fmt.Errorf("write sandbox marker: %w", err)
		}
		return &Sandbox{root: root, name: name}, nil
	}
	return nil, errors.New("could not find an unused sandbox name")
}

// Open returns a handle to an existing sandbox. Anything that is not a marked
// sandbox is refused with ErrNotSandbox.
func Open(dir string) (*Sandbox, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, notSandbox("bad path")
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, notSandbox("cannot resolve directory")
	}
	name, err := checkRoot(real)
	if err != nil {
		return nil, err
	}
	return &Sandbox{root: real, name: name}, nil
}

// Root is the absolute path of the sandbox directory ("" for a nil sandbox).
func (s *Sandbox) Root() string {
	if s == nil {
		return ""
	}
	return s.root
}

// Name is the directory name of the sandbox.
func (s *Sandbox) Name() string {
	if s == nil {
		return ""
	}
	return s.name
}

// Verify re-checks the marker on disk. A nil or zero-value Sandbox fails.
func (s *Sandbox) Verify() error {
	if s == nil || s.root == "" {
		return notSandbox("zero value, not made by Create or Open")
	}
	name, err := checkRoot(s.root)
	if err != nil {
		return err
	}
	if name != s.name {
		return notSandbox("directory name changed")
	}
	return nil
}

// Resolve turns a relative path into an absolute one inside the sandbox. It
// rejects absolute paths, anything that climbs out with .., the marker, and
// any path that passes through a symlink.
func (s *Sandbox) Resolve(rel string) (string, error) {
	if err := s.Verify(); err != nil {
		return "", err
	}
	if rel == "" || strings.ContainsRune(rel, 0) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("%w: %q is not a usable relative path", ErrEscape, rel)
	}
	sep := string(filepath.Separator)
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+sep) {
		return "", fmt.Errorf("%w: %q", ErrEscape, rel)
	}
	if clean == MarkerName || strings.HasPrefix(clean, MarkerName+sep) {
		return "", fmt.Errorf("%w: %q is reserved", ErrEscape, rel)
	}
	cur := s.root
	for _, part := range strings.Split(clean, sep) {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("cannot inspect %q: %w", rel, err)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("%w: %q passes through a symlink", ErrEscape, rel)
		}
	}
	return filepath.Join(s.root, clean), nil
}

// MkdirAll creates a directory (and parents) inside the sandbox.
func (s *Sandbox) MkdirAll(rel string) error {
	full, err := s.Resolve(rel)
	if err != nil {
		return err
	}
	return os.MkdirAll(full, 0o700)
}

// CreateFile creates a new file inside the sandbox. It never overwrites an
// existing file, and it strips group and other permission bits.
func (s *Sandbox) CreateFile(rel string, perm os.FileMode) (*os.File, error) {
	full, err := s.Resolve(rel)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		return nil, err
	}
	perm = (perm & 0o700) | 0o600
	return os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
}

// Remove deletes the sandbox directory, but only after the marker check passes.
func (s *Sandbox) Remove() error {
	if err := s.Verify(); err != nil {
		return err
	}
	return os.RemoveAll(s.root)
}
