package store

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/gmccollu/woodpile/internal/index"
)

// closeJob is a file that was just closed (rotated): its handle is gone, but its data
// still has to be fsynced before it may be compressed or deleted.
type closeJob struct {
	evict   *device // instead: carry out a makeRoomLocked eviction
	e       *catEntry
	path    string
	dir     string
	renamed bool // a part rename: fsync the directory too
}

// queueClose hands a closed file to the closer pool. It never blocks.
func (s *Store) queueClose(j closeJob) {
	s.closeMu.Lock()
	s.closeQ = append(s.closeQ, j)
	s.closeMu.Unlock()
	s.closeCond.Signal()
}

func (s *Store) startClosers() {
	for i := 0; i < s.opts.CloseWorkers; i++ {
		s.closeWG.Add(1)
		go func() {
			defer s.closeWG.Done()
			for {
				j, ok := s.nextClose(true)
				if !ok {
					return
				}
				s.finishClose(j)
			}
		}()
	}
}

// nextClose pops a job; with wait it blocks until there is one or the pool stops.
func (s *Store) nextClose(wait bool) (closeJob, bool) {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	for len(s.closeQ) == 0 {
		if !wait || s.closeStop {
			return closeJob{}, false
		}
		s.closeCond.Wait()
	}
	j := s.closeQ[0]
	s.closeQ[0] = closeJob{}
	s.closeQ = s.closeQ[1:]
	return j, true
}

func (s *Store) stopClosers() {
	s.closeMu.Lock()
	s.closeStop = true
	s.closeMu.Unlock()
	s.closeCond.Broadcast()
	s.closeWG.Wait()
}

// drainClose finishes every queued close in the calling goroutine (Manual mode,
// Flush and Close).
func (s *Store) drainClose() {
	for {
		j, ok := s.nextClose(false)
		if !ok {
			return
		}
		s.finishClose(j)
	}
}

// finishClose fsyncs a closed file and makes it eligible for compression and
// retention. If the fsync fails the file is released anyway (its data is in the OS
// cache, and compression fsyncs its own output), with a warning event.
func (s *Store) finishClose(j closeJob) {
	if j.evict != nil {
		s.finishEvict(j.evict)
		return
	}
	err := s.syncPath(j.path)
	if errors.Is(err, fs.ErrNotExist) {
		err = nil
	}
	if j.renamed {
		syncDir(j.dir)
	}
	s.catMu.Lock()
	j.e.syncing = false
	dev, rel := j.e.deviceID, j.e.rel // the compressor may rename it from here on
	s.catMu.Unlock()
	if err != nil {
		s.event(index.LevelWarn, index.KindError, dev, fmt.Sprintf("fsync %s: %v", rel, err))
	}
	s.kickCompress()
}
