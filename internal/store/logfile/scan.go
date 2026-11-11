package logfile

import (
	"bufio"
	"errors"
	"io"
	"net/netip"
	"time"
)

// ScanOptions configures Scan.
type ScanOptions struct {
	// Start is the offset in the uncompressed stream that the reader is positioned
	// at, and StartLine the number of lines before it. Both are 0 for a full scan.
	Start, StartLine int64
	// Checkpointer is the checkpoint state at Start (see ResumeCheckpointer). Nil
	// starts a fresh sequence, which is right only when Start is 0.
	Checkpointer *Checkpointer
	// OnLine, if set, is called for every complete, well-formed line. The Line's
	// Escaped slice is only valid during the call.
	OnLine func(off int64, l Line) error
}

// Summary is what Scan learned about a file.
type Summary struct {
	Lines     int64      // complete lines, including StartLine
	RawSize   int64      // uncompressed stream length, including Start and any torn tail
	TornBytes int64      // bytes after the last '\n' (a partial line, e.g. after a crash)
	BadLines  int64      // complete lines that didn't parse (counted in Lines)
	MsgBytes  int64      // total length of the unescaped raw messages scanned
	First     time.Time  // lowest received time scanned (zero if none)
	Last      time.Time  // highest received time scanned (zero if none)
	Sender    netip.Addr // sender of the first well-formed line scanned
	// Checkpoints are the checkpoints due in the scanned part, in order.
	Checkpoints []Checkpoint
	// Checkpointer is the state after the last line, for resuming writes or scans.
	Checkpointer Checkpointer
}

// Scan reads lines from r and applies the checkpoint rule. A final line without '\n'
// is reported in TornBytes and otherwise ignored.
func Scan(r io.Reader, opt ScanOptions) (Summary, error) {
	s := Summary{Lines: opt.StartLine, RawSize: opt.Start}
	if opt.Checkpointer != nil {
		s.Checkpointer = *opt.Checkpointer
	}
	br := bufio.NewReaderSize(r, 256<<10)
	var long []byte
	off := opt.Start
	for {
		chunk, err := br.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			long = append(long, chunk...)
			continue
		}
		line := chunk
		if long != nil {
			long = append(long, chunk...)
			line = long
		}
		if err != nil {
			s.RawSize = off + int64(len(line))
			s.TornBytes = int64(len(line))
			if errors.Is(err, io.EOF) {
				return s, nil
			}
			return s, err
		}
		s.Lines++
		if l, perr := ParseLine(line); perr != nil {
			s.BadLines++
		} else {
			if s.First.IsZero() || l.Received.Before(s.First) {
				s.First = l.Received
			}
			if l.Received.After(s.Last) {
				s.Last = l.Received
			}
			if !s.Sender.IsValid() {
				s.Sender = l.Sender
			}
			s.MsgBytes += int64(UnescapedLen(l.Escaped))
			if cp, ok := s.Checkpointer.Observe(off, s.Lines-1, l.Received); ok {
				s.Checkpoints = append(s.Checkpoints, cp)
			}
			if opt.OnLine != nil {
				if err := opt.OnLine(off, l); err != nil {
					return s, err
				}
			}
		}
		off += int64(len(line))
		s.RawSize = off
		long = nil
	}
}

// UnescapedLen returns the length Unescape would produce for esc.
func UnescapedLen(esc []byte) int {
	n := len(esc)
	for i := 0; i < len(esc)-1; i++ {
		if esc[i] == '\\' {
			switch esc[i+1] {
			case 'n', 'r', '\\':
				n--
				i++
			default:
				i++
			}
		}
	}
	return n
}

// ScanFile scans a whole log file, decompressing it if it ends in .gz.
func ScanFile(path string) (Summary, error) {
	rc, err := Open(path)
	if err != nil {
		return Summary{}, err
	}
	defer rc.Close()
	return Scan(rc, ScanOptions{})
}
