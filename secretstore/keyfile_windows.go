//go:build windows

package secretstore

import "os"

func openKeyFile(path string) (*os.File, error) { return os.Open(path) }
func ownedByCurrentUser(os.FileInfo) bool       { return true }
