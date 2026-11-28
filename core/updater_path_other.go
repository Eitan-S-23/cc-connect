//go:build !windows

package core

import "os"

func updatePathIsLink(info os.FileInfo) bool { return info.Mode()&os.ModeSymlink != 0 }
func updateFileInUse(string) (bool, error)   { return false, nil }
