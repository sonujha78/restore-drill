package verify

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sonujha78/restore-drill/internal/config"
)

var hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Querier runs one SQL statement in the sandbox database and returns its output.
// pgbox.DB satisfies it, and a fake satisfies it in unit tests.
type Querier interface {
	Scalar(sql string) (string, error)
}

// PGSource reads a restored PostgreSQL sandbox database. It only asks for
// names, counts, timestamps and hashes, never row contents.
type PGSource struct {
	q Querier
}

// NewPGSource wraps a verified sandbox database.
func NewPGSource(q Querier) *PGSource { return &PGSource{q: q} }

func (p *PGSource) PathExists(string) (bool, error) { return false, ErrUnsupported }
func (p *PGSource) Command(string) (string, error)  { return "", ErrUnsupported }
func (p *PGSource) Integrity() (string, error)      { return "", ErrUnsupported }

// Tables lists the tables and columns of the public schema.
func (p *PGSource) Tables() (map[string][]string, error) {
	out, err := p.q.Scalar(`SELECT table_name || ':' || column_name FROM information_schema.columns WHERE table_schema = 'public' ORDER BY table_name, ordinal_position`)
	if err != nil {
		return nil, err
	}
	res := map[string][]string{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		i := strings.IndexByte(line, ':')
		if i <= 0 || i == len(line)-1 {
			return nil, errors.New("unexpected table listing (output not shown)")
		}
		res[line[:i]] = append(res[line[:i]], line[i+1:])
	}
	return res, nil
}

func (p *PGSource) RowCount(table string) (int64, error) {
	t, err := quote(table)
	if err != nil {
		return 0, err
	}
	out, err := p.q.Scalar("SELECT count(*) FROM " + t)
	if err != nil {
		return 0, err
	}
	out = strings.TrimSpace(out)
	if !numeric.MatchString(out) {
		return 0, errors.New("unexpected row count output (not shown)")
	}
	return strconv.ParseInt(out, 10, 64)
}

// SampleHash hashes the first rows in physical order. Row text is hashed inside
// the database and only the count and the hash come back.
func (p *PGSource) SampleHash(table string, limit int) (string, int, error) {
	t, err := quote(table)
	if err != nil {
		return "", 0, err
	}
	q := fmt.Sprintf(`SELECT count(*) || ' ' || coalesce(encode(sha256(convert_to(string_agg(r, '|' ORDER BY r), 'UTF8')), 'hex'), '') FROM (SELECT x::text AS r FROM %s x ORDER BY ctid LIMIT %d) s`, t, limit)
	out, err := p.q.Scalar(q)
	if err != nil {
		return "", 0, err
	}
	f := strings.Fields(out)
	switch {
	case len(f) == 1 && f[0] == "0":
		return "", 0, nil
	case len(f) == 2 && hex64.MatchString(f[1]):
		n, err := strconv.Atoi(f[0])
		if err == nil && n >= 1 {
			return f[1], n, nil
		}
	}
	return "", 0, errors.New("unexpected sample output (not shown)")
}

// Canary returns the newest canary timestamp, in UTC.
func (p *PGSource) Canary(c config.Canary) (time.Time, error) {
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
	out, err := p.q.Scalar(fmt.Sprintf(`SELECT coalesce(to_char(max(%s) AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'), '') FROM %s`, col, t))
	if err != nil {
		return time.Time{}, err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return time.Time{}, ErrNoCanaryData
	}
	ts, err := time.Parse(time.RFC3339Nano, out)
	if err != nil {
		return time.Time{}, errors.New("canary value is not an RFC 3339 timestamp (value not shown)")
	}
	return ts, nil
}

func (p *PGSource) Scalar(query string) (string, error) { return p.q.Scalar(query) }
