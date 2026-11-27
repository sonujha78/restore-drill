package artifact

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
)

func init() { Register("tar_gz", readTarGz) }

// readTarGz reads the whole archive, every entry and the gzip trailer. The
// trailer holds a CRC, so a flipped bit anywhere in the stream is detected.
func readTarGz(path string) []Result {
	const check = "readable"
	f, err := os.Open(path)
	if err != nil {
		return one(check, Unverified, "cannot open file: "+err.Error())
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return one(check, Fail, "not a readable gzip stream: "+err.Error())
	}
	tr := tar.NewReader(zr)
	entries := 0
	var total int64
	for {
		_, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return one(check, Fail, fmt.Sprintf("tar broke after %d entries: %v", entries, err))
		}
		n, err := io.Copy(io.Discard, tr)
		if err != nil {
			return one(check, Fail, fmt.Sprintf("entry %d is unreadable: %v", entries+1, err))
		}
		total += n
		entries++
	}
	if _, err := io.Copy(io.Discard, zr); err != nil {
		return one(check, Fail, "gzip trailer or checksum failed: "+err.Error())
	}
	if entries == 0 {
		return one(check, Fail, "archive has no entries")
	}
	return one(check, Pass, fmt.Sprintf("gzip and tar read end to end: %d entries, %d bytes", entries, total))
}
