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
		for _, de := range entries {
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
	ok = true
	return root, files, hex.EncodeToString(h.Sum(nil)), nil
}

func (f legacyFile) visit(ctx context.Context, root *os.Root, use func(Record, []byte) error) error {
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
			return errors.New("incomplete or oversized diagnostic segment record")
		}
		var r Record
		if json.Unmarshal(line, &r) != nil || r.ReceivedAt.IsZero() || r.Time.IsZero() || r.UserID <= 0 ||
			r.Seq <= last || !r.Session.valid() {
			return errors.New("invalid diagnostic segment record")
		}
		r.Mode = EffectiveMode(r.Mode)
		if last == 0 {
			key = r.Session.key(r.UserID)
		} else if key != r.Session.key(r.UserID) {
			return errors.New("mixed diagnostic segment")
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
