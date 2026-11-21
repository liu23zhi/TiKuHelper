//go:build !windows

package registry

import "os"

func createPrivateAdminFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
}
