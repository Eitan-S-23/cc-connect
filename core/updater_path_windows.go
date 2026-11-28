//go:build windows

package core

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

func updatePathIsLink(info os.FileInfo) bool {
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return info.Mode()&os.ModeSymlink != 0 || (ok && data.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0)
}

// Restart Manager distinguishes a file held by a live application from an ACL
// error. Never interpret ERROR_ACCESS_DENIED alone as an occupied backup.
func updateFileInUse(path string) (bool, error) {
	dll := syscall.NewLazyDLL("rstrtmgr.dll")
	var session uint32
	var key [33]uint16
	code, _, _ := dll.NewProc("RmStartSession").Call(uintptr(unsafe.Pointer(&session)), 0, uintptr(unsafe.Pointer(&key[0])))
	if code != 0 {
		return false, fmt.Errorf("RmStartSession: %w", syscall.Errno(code))
	}
	defer dll.NewProc("RmEndSession").Call(uintptr(session))
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return false, err
	}
	code, _, _ = dll.NewProc("RmRegisterResources").Call(uintptr(session), 1, uintptr(unsafe.Pointer(&name)), 0, 0, 0, 0)
	if code != 0 {
		return false, fmt.Errorf("RmRegisterResources: %w", syscall.Errno(code))
	}
	var needed, count, reasons uint32
	code, _, _ = dll.NewProc("RmGetList").Call(uintptr(session), uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&count)), 0, uintptr(unsafe.Pointer(&reasons)))
	if code != 0 && code != 234 {
		return false, fmt.Errorf("RmGetList: %w", syscall.Errno(code))
	}
	return needed > 0, nil
}
