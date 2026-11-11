package logfile

import (
	"compress/gzip"
	"io"
	"os"
	"strings"
)

// OpenFile opens a file for reading. On Windows it also allows the file to be renamed
// or deleted while open (FILE_SHARE_DELETE), so a reader never blocks the store's
// rotation, compression or retention. Readers of log files should always use it (or
// Open / OpenAt).
func OpenFile(path string) (*os.File, error) { return openShared(path) }

// Open opens a log file for reading its uncompressed stream: a .gz file is
// decompressed on the fly.
//
// Files can be renamed (a part closing), replaced by their .gz (compression) or
// deleted (retention) at any time. Resolve paths through the index just before
// opening, and on a not-exist error look the file up again by its index ID, which
// stays the same across rename and compression.
func Open(path string) (io.ReadCloser, error) { return OpenAt(path, 0) }

// OpenAt is Open positioned at offset in the uncompressed stream (a checkpoint
// offset). Plain files seek; compressed files decompress and discard up to offset.
func OpenAt(path string, offset int64) (io.ReadCloser, error) {
	f, err := openShared(path)
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(path, ".gz") {
		if offset > 0 {
			if _, err := f.Seek(offset, io.SeekStart); err != nil {
				f.Close()
				return nil, err
			}
		}
		return f, nil
	}
	zr, err := gzip.NewReader(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	rc := &gzReadCloser{Reader: zr, f: f}
	if offset > 0 {
		if _, err := io.CopyN(io.Discard, zr, offset); err != nil {
			rc.Close()
			return nil, err
		}
	}
	return rc, nil
}

type gzReadCloser struct {
	*gzip.Reader
	f *os.File
}

func (g *gzReadCloser) Close() error {
	err := g.Reader.Close()
	if cerr := g.f.Close(); err == nil {
		err = cerr
	}
	return err
}
