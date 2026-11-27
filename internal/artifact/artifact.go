// Package artifact is the first and cheapest verification layer: does the
// backup file exist, is its size plausible, does its checksum match the
// manifest, and can it be read. It never opens a database and never reads
// row contents. Each backup type has one reader in its own file.
package artifact

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"sort"
)

// Status is the outcome of one check. The zero value is Unverified on purpose,
// so a forgotten status can never look like a pass.
type Status int

const (
	Unverified Status = iota
	Pass
	Fail
)

func (s Status) String() string {
	switch s {
	case Pass:
		return "pass"
	case Fail:
		return "fail"
	default:
		return "unverified"
	}
}

// Result is one check outcome. Detail never contains file contents.
type Result struct {
	Check  string
	Status Status
	Detail string
}

// Params describes one file to check.
type Params struct {
	Kind         string
	Path         string
	ManifestPath string
	MinBytes     int64
}

// Reader is the type-specific "can this file be read" check of one adapter.
type Reader func(path string) []Result

var readers = map[string]Reader{}

// Register adds the reader of a backup type. Adapters call it from init().
func Register(kind string, r Reader) { readers[kind] = r }

// Kinds lists the registered backup types, sorted.
func Kinds() []string {
	out := make([]string, 0, len(readers))
	for k := range readers {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Overall is fail if any check failed, else unverified if any check could not
// run (or there were no checks), else pass.
func Overall(rs []Result) Status {
	if len(rs) == 0 {
		return Unverified
	}
	out := Pass
	for _, r := range rs {
		switch r.Status {
		case Fail:
			return Fail
		case Unverified:
			out = Unverified
		}
	}
	return out
}

// Run applies the checks from cheap to expensive. A missing file stops the run.
func Run(p Params) []Result {
	var out []Result
	ex := CheckExists(p.Path)
	out = append(out, ex)
	if ex.Status != Pass {
		return out
	}
	if st, err := os.Stat(p.Path); err == nil {
		out = append(out, CheckSize(st.Size(), p.MinBytes))
	}
	out = append(out, CheckChecksum(p.Path, p.ManifestPath))
	r, ok := readers[p.Kind]
	if !ok {
		return append(out, Result{Check: "readable", Status: Unverified,
			Detail: "no reader registered for type " + p.Kind})
	}
	return append(out, r(p.Path)...)
}

// CheckExists checks that the path is a regular file.
func CheckExists(path string) Result {
	st, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Result{Check: "exists", Status: Fail, Detail: "file does not exist"}
	case err != nil:
		return Result{Check: "exists", Status: Unverified, Detail: "cannot stat file: " + err.Error()}
	case !st.Mode().IsRegular():
		return Result{Check: "exists", Status: Fail, Detail: "not a regular file"}
	}
	return Result{Check: "exists", Status: Pass, Detail: "regular file"}
}

// CheckSize fails on an empty file and on a file below min.
func CheckSize(size, min int64) Result {
	switch {
	case size == 0:
		return Result{Check: "size", Status: Fail, Detail: "file is empty (0 bytes)"}
	case size < min:
		return Result{Check: "size", Status: Fail, Detail: sprintf("%d bytes, below min_bytes %d", size, min)}
	}
	return Result{Check: "size", Status: Pass, Detail: sprintf("%d bytes (min %d)", size, min)}
}

func one(check string, s Status, detail string) []Result {
	return []Result{{Check: check, Status: s, Detail: detail}}
}

func shortFile(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}
