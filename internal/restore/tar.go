package restore

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/sonujha78/restore-drill/internal/sandbox"
)

func init() { Register("tar_gz", restoreTarGz) }

// restoreTarGz extracts regular files and directories only. Symlinks,
// hardlinks, devices and FIFOs are refused, and every path is resolved by the
// sandbox, which rejects .., absolute paths, the marker and symlinks.
func restoreTarGz(src string, sb *sandbox.Sandbox, lim Limits) (Result, error) {
	var res Result
	f, err := os.Open(src)
	if err != nil {
		return res, fmt.Errorf("open backup: %w", err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return res, fmt.Errorf("not a readable gzip stream: %w", err)
	}
	tr := tar.NewReader(zr)
	entries := 0
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return res, fmt.Errorf("tar broke after %d entries: %w", entries, err)
		}
		entries++
		if entries > lim.MaxEntries {
			return res, fmt.Errorf("more than %d entries, refusing", lim.MaxEntries)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := sb.MkdirAll(h.Name); err != nil {
				return res, fmt.Errorf("entry %d: %w", entries, err)
			}
		case tar.TypeReg:
			if res.Bytes+h.Size > lim.MaxBytes {
				return res, fmt.Errorf("restored size would pass %d bytes, refusing", lim.MaxBytes)
			}
			out, err := sb.CreateFile(h.Name, os.FileMode(h.Mode))
			if err != nil {
				return res, fmt.Errorf("entry %d: %w", entries, err)
			}
			n, err := io.Copy(out, io.LimitReader(tr, h.Size))
			cerr := out.Close()
			res.Bytes += n
			if err != nil {
				return res, fmt.Errorf("entry %d is unreadable: %w", entries, err)
			}
			if cerr != nil {
				return res, cerr
			}
			if n != h.Size {
				return res, fmt.Errorf("entry %d is short: %d of %d bytes", entries, n, h.Size)
			}
			res.Files++
		default:
			return res, fmt.Errorf("entry %d has unsupported type %q (only files and directories are restored)", entries, string(h.Typeflag))
		}
	}
	if _, err := io.Copy(io.Discard, zr); err != nil {
		return res, fmt.Errorf("gzip trailer or checksum failed: %w", err)
	}
	return res, nil
}
