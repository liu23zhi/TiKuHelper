package extensionlogs

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var errLegacyChanged = errors.New("diagnostic segment changed during read")

type legacyFile struct {
	name string
	id   string
	info os.FileInfo
}

// Discover the same managed layouts as the old reader. This path is read-only
// and does not run the legacy staging recovery.
func (s *Store) legacyFiles() (*os.Root, []legacyFile, string, error) {
	return s.collectLegacyFiles(false)
}
func (s *Store) collectLegacyFiles(maintenance bool) (*os.Root, []legacyFile, string, error) {
	h := sha256.New()
	empty := hex.EncodeToString(h.Sum(nil))
	root, err := os.OpenRoot(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, empty, nil
	}
	if err != nil {
		return nil, nil, "", err
	}
	ok := false
	defer func() {
		if !ok {
			_ = root.Close()
		}
	}()
	dirs, err := s.segmentDirs()
	if err != nil {
		return nil, nil, "", err
	}
	sort.Strings(dirs)
	var files []legacyFile
	for _, dir := range dirs {
		plain := true
		var path string
		for _, part := range strings.Split(filepath.Clean(dir), string(filepath.Separator)) {
			path = filepath.Join(path, part)
			fi, e := root.Lstat(path)
			if errors.Is(e, os.ErrNotExist) || e == nil && !fi.IsDir() {
				plain = false
				break
			}
			if e != nil {
				return nil, nil, "", e
			}
		}
		if !plain {
			continue
		}
		entries, e := fs.ReadDir(root.FS(), filepath.ToSlash(dir))
		if errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e != nil {
			return nil, nil, "", e
		}
		if maintenance && !s.recoveredStaging && dir != legacyDir {
			if e := s.recoverStaging(dir, entries); e != nil {
				return nil, nil, "", e
			}
		}
		for _, de := range entries {
			if maintenance && de.Type().IsRegular() && retainedStagingName(de.Name()) {
				// Under the single writer's lock, no live rewrite can own
				// this name. A read-only request never enters this branch.
				n := filepath.Join(dir, de.Name())
				if fi, e := root.Lstat(n); e == nil && fi.Mode().IsRegular() {
					if e = root.Remove(n); e != nil && !errors.Is(e, os.ErrNotExist) {
						return nil, nil, "", e
					}
				}
				continue
			}
			if !de.Type().IsRegular() || !segmentRE.MatchString(de.Name()) {
				continue
			}
			fi, e := de.Info()
			if e != nil {
				return nil, nil, "", e
			}
			if fi.Size() == 0 {
				continue
			}
			name := filepath.Join(dir, de.Name())
			id := sha256.Sum256([]byte(name))
			files = append(files, legacyFile{name, hex.EncodeToString(id[:]), fi})
			_, _ = fmt.Fprintf(h, "%s\x00%d\x00%d\n", name, fi.Size(), fi.ModTime().UnixNano())
		}
	}
	if maintenance {
		s.recoveredStaging = true
	}
	ok = true
	return root, files, hex.EncodeToString(h.Sum(nil)), nil
}

// The old initial-marker code is a legacy-only exception to the current
// event allowlist, not a reason to export arbitrary strings as log events.
func validLegacyPayload(r Record) bool {
	e := r.Entry
	if e.Event == "session.start" {
		e.Event = "extension.ready"
	}
	return (Batch{Session: r.Session, Entries: []Entry{e}}).valid()
}

const retainedPrefix = ".retained-"

func retainedStagingName(n string) bool {
	s, ok := strings.CutPrefix(n, retainedPrefix)
	if !ok || len(s) != 24 || strings.ToLower(s) != s {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
func (f legacyFile) visit(ctx context.Context, root *os.Root, use func(Record, []byte) error) error {
	return f.visitRows(ctx, root, false, use)
}
func (f legacyFile) visitRows(ctx context.Context, root *os.Root, repair bool, use func(Record, []byte) error) error {
	in, err := root.Open(f.name)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() || !os.SameFile(fi, f.info) || fi.Size() < f.info.Size() {
		return errLegacyChanged
	}
	scan := bufio.NewReaderSize(io.NewSectionReader(in, 0, f.info.Size()), 8192)
	var last int64
	var key sessionKey
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		line, e := scan.ReadSlice('\n')
		if e == io.EOF && len(line) == 0 {
			break
		}
		if e != nil {
			if !repair || e != io.EOF && e != bufio.ErrBufferFull {
				return errors.New("incomplete or oversized diagnostic segment record")
			}
			if err = use(Record{}, nil); err != nil {
				return err
			}
			// Drain a single unclassifiable oversized line, bounded by the
			// same section reader, before attempting the next real record.
			for e == bufio.ErrBufferFull {
				if err = ctx.Err(); err != nil {
					return err
				}
				_, e = scan.ReadSlice('\n')
				if e != nil && e != io.EOF && e != bufio.ErrBufferFull {
					return e
				}
			}
			continue
		}
		var r Record
		if json.Unmarshal(line, &r) != nil || r.ReceivedAt.IsZero() || r.Time.IsZero() || r.UserID <= 0 ||
			r.Seq <= last || !validLegacyPayload(r) || last != 0 && key != r.Session.key(r.UserID) {
			if !repair {
				return errors.New("invalid or mixed diagnostic segment record")
			}
			if e := use(Record{}, nil); e != nil {
				return e
			}
			continue
		}
		r.Mode = EffectiveMode(r.Mode)
		if last == 0 {
			key = r.Session.key(r.UserID)
		}
		last = r.Seq
		if err = use(r, line); err != nil {
			return err
		}
	}
	after, err := root.Lstat(f.name)
	if err != nil || !os.SameFile(after, f.info) || after.Size() < f.info.Size() ||
		after.Size() == f.info.Size() && !after.ModTime().Equal(f.info.ModTime()) {
		return errLegacyChanged
	}
	return nil
}
