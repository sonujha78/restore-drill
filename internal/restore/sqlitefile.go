package restore

import (
	"fmt"
	"io"
	"os"

	"github.com/sonujha78/restore-drill/internal/sandbox"
)

// SQLiteFile is the name the restored database gets inside the sandbox.
const SQLiteFile = "restored.db"

func init() { Register("sqlite", restoreSQLite) }

// restoreSQLite copies the database file into the sandbox as restored.db.
// Opening it as a database happens in the later structure and content checks.
func restoreSQLite(src string, sb *sandbox.Sandbox, lim Limits) (Result, error) {
	var res Result
	in, err := os.Open(src)
	if err != nil {
		return res, fmt.Errorf("open backup: %w", err)
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return res, err
	}
	if st.Size() > lim.MaxBytes {
		return res, fmt.Errorf("backup is %d bytes, above the limit %d", st.Size(), lim.MaxBytes)
	}
	out, err := sb.CreateFile(SQLiteFile, 0o600)
	if err != nil {
		return res, err
	}
	n, err := io.Copy(out, in)
	cerr := out.Close()
	res.Bytes = n
	if err != nil {
		return res, fmt.Errorf("copy failed: %w", err)
	}
	if cerr != nil {
		return res, cerr
	}
	res.Files = 1
	return res, nil
}
