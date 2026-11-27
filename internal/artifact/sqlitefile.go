package artifact

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

func init() { Register("sqlite", readSQLite) }

// readSQLite checks the 100-byte header and that the file holds exactly the
// number of pages the header promises. The SQLite engine is not used here;
// the full integrity_check runs after restore.
func readSQLite(path string) []Result {
	f, err := os.Open(path)
	if err != nil {
		return one("header", Unverified, "cannot open file: "+err.Error())
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return one("header", Unverified, "cannot stat file: "+err.Error())
	}
	hdr := make([]byte, 100)
	if _, err := io.ReadFull(f, hdr); err != nil {
		if shortFile(err) {
			return one("header", Fail, "file is smaller than the 100-byte SQLite header")
		}
		return one("header", Unverified, "cannot read header: "+err.Error())
	}
	if string(hdr[:16]) != "SQLite format 3\x00" {
		return one("header", Fail, "bad header magic, not a SQLite database")
	}
	ps := int64(binary.BigEndian.Uint16(hdr[16:18]))
	if ps == 1 {
		ps = 65536
	}
	if ps < 512 || ps&(ps-1) != 0 {
		return one("header", Fail, fmt.Sprintf("invalid page size %d", ps))
	}
	out := one("header", Pass, fmt.Sprintf("magic ok, page size %d", ps))

	counter := binary.BigEndian.Uint32(hdr[24:28])
	pages := binary.BigEndian.Uint32(hdr[28:32])
	validFor := binary.BigEndian.Uint32(hdr[92:96])
	size := st.Size()
	switch {
	case size%ps != 0:
		return append(out, Result{Check: "readable", Status: Fail,
			Detail: fmt.Sprintf("file size %d is not a multiple of the page size %d (truncated or padded)", size, ps)})
	case pages == 0 || counter != validFor:
		return append(out, Result{Check: "readable", Status: Unverified,
			Detail: "header page count is not trustworthy, truncation cannot be ruled out at this layer"})
	case int64(pages)*ps != size:
		return append(out, Result{Check: "readable", Status: Fail,
			Detail: fmt.Sprintf("header says %d pages, file holds %d (truncated)", pages, size/ps)})
	}
	return append(out, Result{Check: "readable", Status: Pass,
		Detail: fmt.Sprintf("%d pages, size matches header (full integrity_check runs after restore)", pages)})
}
