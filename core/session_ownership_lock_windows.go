//go:build windows

package core

import (
	"fmt"
	"syscall"
	"time"
)

const (
	winErrorSharingViolation = syscall.Errno(32)
	winErrorLockViolation    = syscall.Errno(33)
)

func acquireSessionOwnershipFileLock(path string) (func(), error) {
	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		handle, createErr := syscall.CreateFile(
			pathPtr,
			syscall.GENERIC_READ|syscall.GENERIC_WRITE,
			0,
			nil,
			syscall.OPEN_ALWAYS,
			syscall.FILE_ATTRIBUTE_NORMAL,
			0,
		)
		if createErr == nil {
			return func() { _ = syscall.CloseHandle(handle) }, nil
		}
		if createErr != winErrorSharingViolation && createErr != winErrorLockViolation {
			return nil, createErr
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for %s", path)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
