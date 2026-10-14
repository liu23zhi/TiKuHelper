package logger

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type rotatingFile struct {
	dir      string
	name     string
	path     string
	file     *os.File
	size     int64
	maxBytes int64
	sequence int
}

func openRotatingFile(dir, name string, maxBytes int64) (*rotatingFile, error) {
	w := &rotatingFile{dir: dir, name: name, path: filepath.Join(dir, name+".txt"), maxBytes: maxBytes}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		filename := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(filename, name) || !isManagedLogArchive(filename) {
			continue
		}
		digits := strings.TrimSuffix(strings.TrimSuffix(strings.TrimPrefix(filename, name), ".txt.gz"), ".txt")
		if digits == "" || strings.IndexFunc(digits, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
			continue
		}
		sequence, err := strconv.Atoi(digits)
		if err == nil && sequence > w.sequence {
			w.sequence = sequence
		}
	}
	if err := w.reopen(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *rotatingFile) activePath() string { return w.path }

func (w *rotatingFile) reopen() error {
	file, err := os.OpenFile(w.activePath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return err
	}
	w.file, w.size = file, info.Size()
	return nil
}

func (w *rotatingFile) write(p []byte) error {
	var rotationErr error
	if w.size > 0 && (w.size >= w.maxBytes || int64(len(p)) > w.maxBytes-w.size) {
		rotationErr = w.rotate()
	}
	if w.file == nil {
		if err := w.reopen(); err != nil {
			return errors.Join(rotationErr, err)
		}
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	return errors.Join(rotationErr, err)
}

func (w *rotatingFile) rotate() error {
	sequence := w.sequence + 1
	archive := ""
	for {
		archive = filepath.Join(w.dir, fmt.Sprintf("%s%02d.txt", w.name, sequence))
		_, archiveErr := os.Lstat(archive)
		_, gzipErr := os.Lstat(archive + ".gz")
		if os.IsNotExist(archiveErr) && os.IsNotExist(gzipErr) {
			break
		}
		if archiveErr != nil && !os.IsNotExist(archiveErr) {
			return archiveErr
		}
		if gzipErr != nil && !os.IsNotExist(gzipErr) {
			return gzipErr
		}
		sequence++
	}
	// Windows does not permit renaming the active file while it is open.
	if err := w.close(); err != nil {
		return errors.Join(err, w.reopen())
	}
	if err := os.Rename(w.activePath(), archive); err != nil {
		return errors.Join(err, w.reopen())
	}
	w.sequence = sequence
	if err := w.reopen(); err != nil {
		// Keep writing the original file if creating its replacement fails.
		rollbackErr := os.Rename(archive, w.activePath())
		return errors.Join(err, rollbackErr, w.reopen())
	}
	return nil
}

func (w *rotatingFile) close() error {
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}
