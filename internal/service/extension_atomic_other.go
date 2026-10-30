//go:build !windows

package service

import "os"

func replaceExtensionFile(source, target string) error {
	return os.Rename(source, target)
}
