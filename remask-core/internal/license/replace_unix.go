//go:build !windows

package license

import "os"

func replaceFile(source, destination string) error {
	return os.Rename(source, destination)
}
