// Package adapters registers the backup types with the drill. Each type is one
// block below: how to make a sandbox, restore into it, and read it back.
// The artifact reader of a type lives in internal/artifact and registers itself.
package adapters

import (
	"errors"
	"fmt"
	"os"

	"github.com/sonujha78/restore-drill/internal/config"
	"github.com/sonujha78/restore-drill/internal/drill"
	"github.com/sonujha78/restore-drill/internal/pgbox"
	"github.com/sonujha78/restore-drill/internal/restore"
	"github.com/sonujha78/restore-drill/internal/sandbox"
	"github.com/sonujha78/restore-drill/internal/verify"
)

func init() {
	drill.Register("sqlite", drill.Adapter{New: newFile("sqlite")})
	drill.Register("tar_gz", drill.Adapter{New: newFile("tar_gz")})
	drill.Register("postgres_dump", drill.Adapter{New: newPG, RestoreProvesReadable: true})
}

// ---------- file based types: a sandbox directory ----------

type fileSession struct {
	kind string
	env  drill.Env
	sb   *sandbox.Sandbox
	db   *verify.SQLiteSource
}

func newFile(kind string) drill.Factory {
	return func(t *config.Target, env drill.Env) (drill.Session, error) {
		if env.SandboxBase == "" {
			return nil, errors.New("no sandbox base directory given")
		}
		if err := os.MkdirAll(env.SandboxBase, 0o700); err != nil {
			return nil, err
		}
		sb, err := sandbox.Create(env.SandboxBase, env.Clock)
		if err != nil {
			return nil, err
		}
		return &fileSession{kind: kind, env: env, sb: sb}, nil
	}
}

func (s *fileSession) Restore(file string) error {
	_, err := restore.Run(s.kind, file, s.sb, restore.DefaultLimits(), s.env.Clock)
	return err
}

func (s *fileSession) Source() (verify.Source, error) {
	if s.kind == "sqlite" {
		db, err := verify.OpenSQLite(s.sb.Root() + string(os.PathSeparator) + restore.SQLiteFile)
		if err != nil {
			return nil, err
		}
		s.db = db
		return db, nil
	}
	return verify.NewFileSource(s.sb.Root()), nil
}

func (s *fileSession) Close() error {
	var first error
	if s.db != nil {
		first = s.db.Close()
	}
	if err := s.sb.Remove(); err != nil && first == nil {
		first = err
	}
	return first
}

// ---------- postgres_dump: a throwaway database inside an rdr- container ----------

type pgSession struct {
	db *pgbox.DB
}

func newPG(t *config.Target, env drill.Env) (drill.Session, error) {
	r, err := pgbox.NewDockerRunner(t.Options["container"])
	if err != nil {
		return nil, fmt.Errorf("refusing: %w", err)
	}
	db, err := pgbox.Create(r, env.Clock)
	if err != nil {
		return nil, err
	}
	return &pgSession{db: db}, nil
}

func (s *pgSession) Restore(file string) error {
	_, err := s.db.Restore(file, restore.DefaultLimits().MaxBytes)
	return err
}

func (s *pgSession) Source() (verify.Source, error) { return verify.NewPGSource(s.db), nil }
func (s *pgSession) Close() error                   { return s.db.Drop() }
