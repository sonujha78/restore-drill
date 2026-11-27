package artifact

import (
	"fmt"
	"io"
	"os"
)

func init() { Register("postgres_dump", readPGDump) }

// readPGDump checks the header of a custom-format dump (pg_dump -Fc). The header
// is only the first bytes, so a truncated dump still passes it. Whether the
// dump can be read end to end needs pg_restore --list, which is not run at
// this layer, so that check is reported as unverified.
func readPGDump(path string) []Result {
	f, err := os.Open(path)
	if err != nil {
		return one("header", Unverified, "cannot open file: "+err.Error())
	}
	defer f.Close()
	hdr := make([]byte, 11)
	if _, err := io.ReadFull(f, hdr); err != nil {
		if shortFile(err) {
			return one("header", Fail, "file is shorter than a pg_dump header")
		}
		return one("header", Unverified, "cannot read header: "+err.Error())
	}
	if string(hdr[:5]) != "PGDMP" {
		return one("header", Fail, "bad header magic, not a pg_dump archive")
	}
	if hdr[5] != 1 {
		return one("header", Fail, fmt.Sprintf("unsupported archive version %d.%d", hdr[5], hdr[6]))
	}
	if hdr[10] != 1 {
		return one("header", Fail, "archive is not custom format (pg_dump -Fc)")
	}
	return []Result{
		{Check: "header", Status: Pass, Detail: fmt.Sprintf("custom-format archive, version %d.%d", hdr[5], hdr[6])},
		{Check: "readable", Status: Unverified,
			Detail: "pg_restore --list has not been run; a truncated dump cannot be detected by the header alone"},
	}
}
