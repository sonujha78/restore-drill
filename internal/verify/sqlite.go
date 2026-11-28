package verify

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"time"

	"github.com/sonujha78/restore-drill/internal/config"
)

var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)

func quote(ident string) (string, error) {
	if !identRe.MatchString(ident) {
		return "", errors.New("not a valid identifier")
	}
	return `"` + ident + `"`, nil
}

// SQLiteSource reads a restored SQLite file. The connection is read-only, so a
// check can never change the restored data, and a missing file is never created.
type SQLiteSource struct {
	db *sql.DB
}

// OpenSQLite opens the database file read-only.
func OpenSQLite(path string) (*SQLiteSource, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: abs, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return &SQLiteSource{db: db}, nil
}

// Close closes the database.
func (s *SQLiteSource) Close() error { return s.db.Close() }

func (s *SQLiteSource) column(query string, args ...any) ([]string, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *SQLiteSource) Tables() (map[string][]string, error) {
	names, err := s.column(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, n := range names {
		cols, err := s.column(`SELECT name FROM pragma_table_info(?)`, n)
		if err != nil {
			return nil, err
		}
		out[n] = cols
	}
	return out, nil
}

func (s *SQLiteSource) PathExists(string) (bool, error) { return false, ErrUnsupported }
func (s *SQLiteSource) Command(string) (string, error)  { return "", ErrUnsupported }

func (s *SQLiteSource) RowCount(table string) (int64, error) {
	t, err := quote(table)
	if err != nil {
		return 0, err
	}
	var n int64
	if err := s.db.QueryRow("SELECT count(*) FROM " + t).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// SampleHash hashes the first rows by rowid. Values are hashed, never returned.
func (s *SQLiteSource) SampleHash(table string, limit int) (string, int, error) {
	t, err := quote(table)
	if err != nil {
		return "", 0, err
	}
	rows, err := s.db.Query("SELECT * FROM "+t+" ORDER BY rowid LIMIT ?", limit)
	if err != nil {
		return "", 0, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	n := 0
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return "", 0, err
		}
		for _, v := range vals {
			if b, ok := v.([]byte); ok {
				h.Write(b)
			} else {
				fmt.Fprintf(h, "%v", v)
			}
			h.Write([]byte{0x1f})
		}
		h.Write([]byte{0x1e})
		n++
	}
	if err := rows.Err(); err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// Canary returns the newest value of the canary column. "Newest" is the largest
// value as text, which is right when all timestamps share one format.
func (s *SQLiteSource) Canary(c config.Canary) (time.Time, error) {
	if c.Table == "" {
		return time.Time{}, ErrUnsupported
	}
	t, err := quote(c.Table)
	if err != nil {
		return time.Time{}, err
	}
	col, err := quote(c.Column)
	if err != nil {
		return time.Time{}, err
	}
	var v any
	err = s.db.QueryRow("SELECT " + col + " FROM " + t + " ORDER BY " + col + " DESC LIMIT 1").Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, ErrNoCanaryData
	}
	if err != nil {
		return time.Time{}, err
	}
	var text string
	switch x := v.(type) {
	case time.Time:
		return x, nil
	case string:
		text = x
	case []byte:
		text = string(x)
	default:
		return time.Time{}, errors.New("canary value is not a timestamp (value not shown)")
	}
	ts, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return time.Time{}, errors.New("canary value is not an RFC 3339 timestamp (value not shown)")
	}
	return ts, nil
}

func (s *SQLiteSource) Scalar(query string) (string, error) {
	var v any
	if err := s.db.QueryRow(query).Scan(&v); err != nil {
		return "", err
	}
	if b, ok := v.([]byte); ok {
		return string(b), nil
	}
	return fmt.Sprint(v), nil
}

func (s *SQLiteSource) Integrity() (string, error) {
	var v string
	if err := s.db.QueryRow("PRAGMA integrity_check").Scan(&v); err != nil {
		return "", err
	}
	return v, nil
}
