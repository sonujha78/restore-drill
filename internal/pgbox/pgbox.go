// Package pgbox is the PostgreSQL counterpart of the sandbox package. A sandbox
// here is a throwaway database inside an rdr- container. A database is a
// sandbox only if its name has the generated form AND its comment carries a
// marker naming itself. Every operation re-checks that, so the source database
// (rdrdb), postgres and the templates can never be restored into or dropped.
package pgbox

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/sonujha78/restore-drill/internal/clock"
)

const (
	// DBPrefix starts the name of every sandbox database.
	DBPrefix     = "rdr_sbx_"
	commentMagic = "restore-drill sandbox v1 id="
	adminDB      = "postgres"
	pgUser       = "rdr"
)

var (
	// ErrNotSandbox means the database is not a marked restore-drill sandbox.
	ErrNotSandbox = errors.New("not a restore-drill sandbox database")

	nameRe      = regexp.MustCompile(`^rdr_sbx_[0-9]{8}t[0-9]{6}z_[0-9a-f]{8}$`)
	containerRe = regexp.MustCompile(`^rdr-[a-z0-9][a-z0-9_.-]{0,62}$`)
	quoted      = regexp.MustCompile(`"[^"]*"`)

	safePrefixes = []string{"pg_restore: error:", "pg_restore: warning:", "psql: error:", "ERROR:", "FATAL:", "Error response from daemon:"}
)

// Runner runs a command inside the PostgreSQL container.
type Runner interface {
	Run(stdin io.Reader, args ...string) (string, error)
}

// DockerRunner runs commands with docker exec, in one rdr- container only.
type DockerRunner struct {
	container string
}

// NewDockerRunner refuses every container whose name does not start with rdr-,
// so a command can never reach another project's container.
func NewDockerRunner(container string) (*DockerRunner, error) {
	if !containerRe.MatchString(container) {
		return nil, fmt.Errorf("container name must match ^rdr-[a-z0-9][a-z0-9_.-]*$ (only rdr- containers are ever touched)")
	}
	return &DockerRunner{container: container}, nil
}

// Run executes args inside the container. Passwords are never needed: psql and
// pg_restore run as the container's own user over the local socket.
func (d *DockerRunner) Run(stdin io.Reader, args ...string) (string, error) {
	if d == nil || d.container == "" {
		return "", errors.New("no container configured")
	}
	cmd := exec.Command("docker", append([]string{"exec", "-i", d.container}, args...)...)
	cmd.Stdin = stdin
	var out, eb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &eb
	if err := cmd.Run(); err != nil {
		return strings.TrimSpace(out.String()), fmt.Errorf("%w: %s", err, SafeError(eb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// SafeError keeps one recognised error line, cut to 200 characters, with text
// inside double quotes replaced. CONTEXT and DETAIL lines can hold row data and
// are never kept. This is a heuristic: an unquoted value could still slip through.
func SafeError(stderr string) string {
	for _, line := range strings.Split(stderr, "\n") {
		l := strings.TrimSpace(line)
		for _, p := range safePrefixes {
			if strings.HasPrefix(l, p) {
				l = quoted.ReplaceAllString(l, `"<redacted>"`)
				if len(l) > 200 {
					l = l[:200]
				}
				return l
			}
		}
	}
	return "no recognised error line, details withheld"
}

// DB is a handle to a verified sandbox database.
type DB struct {
	r    Runner
	name string
}

func notSandbox(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrNotSandbox, fmt.Sprintf(format, a...))
}

func psql(r Runner, db, sql string) (string, error) {
	return r.Run(nil, "psql", "-U", pgUser, "-d", db, "-v", "ON_ERROR_STOP=1", "-tA", "-c", sql)
}

// Create makes a new, uniquely named database and writes its marker comment.
func Create(r Runner, clk clock.Clock) (*DB, error) {
	if r == nil {
		return nil, errors.New("no runner")
	}
	var rnd [4]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return nil, fmt.Errorf("random id: %w", err)
	}
	name := DBPrefix + strings.ToLower(clk.Now().UTC().Format("20060102T150405Z")) + "_" + hex.EncodeToString(rnd[:])
	if _, err := psql(r, adminDB, "CREATE DATABASE "+name); err != nil {
		return nil, fmt.Errorf("create sandbox database: %w", err)
	}
	if _, err := psql(r, adminDB, fmt.Sprintf("COMMENT ON DATABASE %s IS '%s%s'", name, commentMagic, name)); err != nil {
		_, _ = psql(r, adminDB, "DROP DATABASE IF EXISTS "+name) // our own, just created, unmarked database
		return nil, fmt.Errorf("write sandbox marker: %w", err)
	}
	return &DB{r: r, name: name}, nil
}

// Open returns a handle to an existing sandbox database. Anything else is
// refused with ErrNotSandbox. A bad name is refused before any command runs.
func Open(r Runner, name string) (*DB, error) {
	if r == nil {
		return nil, notSandbox("no runner")
	}
	if !nameRe.MatchString(name) {
		return nil, notSandbox("name does not have the form of a sandbox database")
	}
	d := &DB{r: r, name: name}
	if err := d.Verify(); err != nil {
		return nil, err
	}
	return d, nil
}

// Name is the database name.
func (d *DB) Name() string {
	if d == nil {
		return ""
	}
	return d.name
}

// Verify re-reads the marker comment from PostgreSQL. A nil or zero-value DB fails.
func (d *DB) Verify() error {
	if d == nil || d.r == nil || d.name == "" {
		return notSandbox("zero value, not made by Create or Open")
	}
	if !nameRe.MatchString(d.name) {
		return notSandbox("name does not have the form of a sandbox database")
	}
	got, err := psql(d.r, adminDB, fmt.Sprintf("SELECT shobj_description(oid,'pg_database') FROM pg_database WHERE datname='%s'", d.name))
	if err != nil {
		return notSandbox("cannot read the marker: %v", err)
	}
	if got != commentMagic+d.name {
		return notSandbox("database is missing or carries no matching marker")
	}
	return nil
}

// Restore feeds a custom-format dump to pg_restore, into this database only.
// It returns the number of bytes read from the file.
func (d *DB) Restore(src string, maxBytes int64) (int64, error) {
	if err := d.Verify(); err != nil {
		return 0, err
	}
	f, err := os.Open(src)
	if err != nil {
		return 0, fmt.Errorf("open backup: %w", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if st.Size() > maxBytes {
		return 0, fmt.Errorf("backup is %d bytes, above the limit %d", st.Size(), maxBytes)
	}
	if _, err := d.r.Run(f, "pg_restore", "-U", pgUser, "-d", d.name, "--no-owner", "--no-privileges", "--exit-on-error"); err != nil {
		return st.Size(), fmt.Errorf("pg_restore failed: %w", err)
	}
	return st.Size(), nil
}

// Scalar runs a query inside the sandbox database and returns its single output
// value. Callers use it for counts and hashes and must never print row data.
func (d *DB) Scalar(sql string) (string, error) {
	if err := d.Verify(); err != nil {
		return "", err
	}
	return psql(d.r, d.name, sql)
}

// Drop deletes the database, but only after the marker check passes.
func (d *DB) Drop() error {
	if err := d.Verify(); err != nil {
		return err
	}
	if _, err := psql(d.r, adminDB, "DROP DATABASE "+d.name+" WITH (FORCE)"); err != nil {
		return fmt.Errorf("drop sandbox database: %w", err)
	}
	return nil
}
