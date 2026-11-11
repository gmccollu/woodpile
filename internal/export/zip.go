package export

import (
	"bufio"
	"compress/flate"
	"encoding/binary"
	"errors"
	"hash"
	"hash/crc32"
	"io"
	"os"
	"time"
)

// zipWriter writes a zip file in one pass with bounded memory, to a seekable
// file: each entry's data streams through deflate, and its CRC and sizes are
// patched into the local header afterwards, so there are no data descriptors.
//
// Zip64 (APPNOTE 4.5) is used where a value reaches zip64Limit (0xFFFFFFFF in
// production; tests lower it): per-entry sizes and offsets go into zip64 extra
// fields, and the zip64 end records are written when the entry count, the
// central directory's size or its offset need them. An entry whose size might
// reach the limit gets its zip64 extra in the local header up front (from the
// size hint), because the header can't grow after its data is written.
type zipWriter struct {
	f       *os.File
	w       *bufio.Writer
	off     int64 // bytes written so far
	limit   int64
	mod     time.Time
	entries []zipEntry
	cur     *zipEntry
	fw      *flate.Writer
	level   int
	crc     hash.Hash32
	usize   int64
	err     error
}

type zipEntry struct {
	name         string
	off          int64 // of the local header
	crc          uint32
	csize, usize int64
	zip64Local   bool
}

func newZipWriter(f *os.File, limit int64, mod time.Time, level int) *zipWriter {
	if limit <= 0 || limit > 0xFFFFFFFF {
		limit = 0xFFFFFFFF
	}
	z := &zipWriter{f: f, limit: limit, mod: mod, level: level, crc: crc32.NewIEEE()}
	z.w = bufio.NewWriterSize(offWriter{z}, 256<<10)
	return z
}

// offWriter writes at the zip writer's running offset with WriteAt, so header
// patches (also WriteAt) never move a file position the stream depends on.
type offWriter struct{ z *zipWriter }

func (o offWriter) Write(p []byte) (int, error) {
	n, err := o.z.f.WriteAt(p, o.z.off)
	o.z.off += int64(n)
	return n, err
}

// pos is the offset of the next byte written through z.w.
func (z *zipWriter) pos() int64 { return z.off + int64(z.w.Buffered()) }

// create starts an entry. sizeHint is an upper bound on its uncompressed size
// (or -1 if unknown, which reserves zip64 in the header).
func (z *zipWriter) create(name string, sizeHint int64) error {
	if z.err != nil {
		return z.err
	}
	if z.cur != nil {
		if err := z.closeEntry(); err != nil {
			return err
		}
	}
	e := zipEntry{name: name, off: z.pos()}
	// Deflate can grow incompressible data by a few bytes per 16 KiB block;
	// doubling the hint leaves plenty of room either way.
	e.zip64Local = sizeHint < 0 || sizeHint >= z.limit/2
	var h []byte
	h = le32(h, 0x04034b50)
	h = le16(h, versionNeeded(e.zip64Local))
	h = le16(h, 1<<11) // UTF-8 names
	h = le16(h, 8)     // deflate
	t, d := dosTime(z.mod)
	h = le16(h, t)
	h = le16(h, d)
	h = le32(h, 0) // crc, patched
	if e.zip64Local {
		h = le32(h, 0xFFFFFFFF)
		h = le32(h, 0xFFFFFFFF)
	} else {
		h = le32(h, 0) // sizes, patched
		h = le32(h, 0)
	}
	h = le16(h, uint16(len(name)))
	extra := 0
	if e.zip64Local {
		extra = 20
	}
	h = le16(h, uint16(extra))
	h = append(h, name...)
	if e.zip64Local {
		h = le16(h, 0x0001)
		h = le16(h, 16)
		h = le64(h, 0) // usize, patched
		h = le64(h, 0) // csize, patched
	}
	if _, err := z.w.Write(h); err != nil {
		z.err = err
		return err
	}
	z.entries = append(z.entries, e)
	z.cur = &z.entries[len(z.entries)-1]
	z.crc.Reset()
	z.usize = 0
	if z.fw == nil {
		z.fw, _ = flate.NewWriter(z.w, z.level)
	} else {
		z.fw.Reset(z.w)
	}
	return nil
}

// Write writes to the current entry.
func (z *zipWriter) Write(p []byte) (int, error) {
	if z.err != nil {
		return 0, z.err
	}
	if z.cur == nil {
		return 0, errors.New("zip: no entry")
	}
	z.crc.Write(p)
	z.usize += int64(len(p))
	n, err := z.fw.Write(p)
	if err != nil {
		z.err = err
	}
	return n, err
}

var errTooBig = errors.New("zip: an entry outgrew its size estimate")

// closeEntry finishes the current entry and patches its local header.
func (z *zipWriter) closeEntry() error {
	e := z.cur
	z.cur = nil
	if err := z.fw.Close(); err != nil {
		z.err = err
		return err
	}
	if err := z.w.Flush(); err != nil {
		z.err = err
		return err
	}
	dataStart := e.off + 30 + int64(len(e.name))
	if e.zip64Local {
		dataStart += 20
	}
	e.crc, e.usize, e.csize = z.crc.Sum32(), z.usize, z.off-dataStart
	if !e.zip64Local && (e.usize >= z.limit || e.csize >= z.limit) {
		z.err = errTooBig
		return z.err
	}
	var p []byte
	p = le32(p, e.crc)
	if !e.zip64Local {
		p = le32(p, uint32(e.csize))
		p = le32(p, uint32(e.usize))
	}
	if _, err := z.f.WriteAt(p, e.off+14); err != nil {
		z.err = err
		return err
	}
	if e.zip64Local {
		var x []byte
		x = le64(x, uint64(e.usize))
		x = le64(x, uint64(e.csize))
		if _, err := z.f.WriteAt(x, e.off+30+int64(len(e.name))+4); err != nil {
			z.err = err
			return err
		}
	}
	return nil
}

// close finishes the last entry and writes the central directory.
func (z *zipWriter) close() error {
	if z.err != nil {
		return z.err
	}
	if z.cur != nil {
		if err := z.closeEntry(); err != nil {
			return err
		}
	}
	cdStart := z.pos()
	t, d := dosTime(z.mod)
	for _, e := range z.entries {
		var x []byte
		// An entry with zip64 sizes in its local header has them here too, so the
		// two headers agree.
		sizes64 := e.zip64Local || e.usize >= z.limit || e.csize >= z.limit
		if sizes64 {
			x = le64(x, uint64(e.usize))
			x = le64(x, uint64(e.csize))
		}
		if e.off >= z.limit {
			x = le64(x, uint64(e.off))
		}
		zip64 := len(x) > 0
		var h []byte
		h = le32(h, 0x02014b50)
		h = le16(h, 3<<8|versionNeeded(zip64 || e.zip64Local)) // made by: Unix
		h = le16(h, versionNeeded(zip64 || e.zip64Local))
		h = le16(h, 1<<11)
		h = le16(h, 8)
		h = le16(h, t)
		h = le16(h, d)
		h = le32(h, e.crc)
		if sizes64 {
			h = le32(h, 0xFFFFFFFF)
			h = le32(h, 0xFFFFFFFF)
		} else {
			h = le32(h, uint32(e.csize))
			h = le32(h, uint32(e.usize))
		}
		h = le16(h, uint16(len(e.name)))
		if zip64 {
			h = le16(h, uint16(4+len(x)))
		} else {
			h = le16(h, 0)
		}
		h = le16(h, 0) // comment
		h = le16(h, 0) // disk
		h = le16(h, 0) // internal attributes
		h = le32(h, 0o100644<<16)
		h = le32(h, clamp32(e.off, z.limit))
		h = append(h, e.name...)
		if zip64 {
			h = le16(h, 0x0001)
			h = le16(h, uint16(len(x)))
			h = append(h, x...)
		}
		if _, err := z.w.Write(h); err != nil {
			return err
		}
	}
	cdEnd := z.pos()
	cdSize := cdEnd - cdStart
	n := int64(len(z.entries))
	var h []byte
	if n >= 0xFFFF || cdSize >= z.limit || cdStart >= z.limit {
		// zip64 end of central directory record and locator.
		h = le32(h, 0x06064b50)
		h = le64(h, 44)
		h = le16(h, 3<<8|45)
		h = le16(h, 45)
		h = le32(h, 0)
		h = le32(h, 0)
		h = le64(h, uint64(n))
		h = le64(h, uint64(n))
		h = le64(h, uint64(cdSize))
		h = le64(h, uint64(cdStart))
		h = le32(h, 0x07064b50)
		h = le32(h, 0)
		h = le64(h, uint64(cdEnd))
		h = le32(h, 1)
	}
	h = le32(h, 0x06054b50)
	h = le16(h, 0)
	h = le16(h, 0)
	cnt := uint16(min(n, 0xFFFF))
	if n >= 0xFFFF {
		cnt = 0xFFFF
	}
	h = le16(h, cnt)
	h = le16(h, cnt)
	h = le32(h, clamp32(cdSize, z.limit))
	h = le32(h, clamp32(cdStart, z.limit))
	h = le16(h, 0)
	if _, err := z.w.Write(h); err != nil {
		return err
	}
	return z.w.Flush()
}

func clamp32(v, limit int64) uint32 {
	if v >= limit {
		return 0xFFFFFFFF
	}
	return uint32(v)
}

func versionNeeded(zip64 bool) uint16 {
	if zip64 {
		return 45
	}
	return 20
}

func dosTime(t time.Time) (uint16, uint16) {
	t = t.UTC()
	if t.Year() < 1980 {
		t = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return uint16(t.Hour()<<11 | t.Minute()<<5 | t.Second()/2),
		uint16((t.Year()-1980)<<9 | int(t.Month())<<5 | t.Day())
}

func le16(b []byte, v uint16) []byte { return binary.LittleEndian.AppendUint16(b, v) }
func le32(b []byte, v uint32) []byte { return binary.LittleEndian.AppendUint32(b, v) }
func le64(b []byte, v uint64) []byte { return binary.LittleEndian.AppendUint64(b, v) }

var _ io.Writer = (*zipWriter)(nil)
