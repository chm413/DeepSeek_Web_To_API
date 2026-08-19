//go:build windows

package config

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procReplaceFileW = kernel32.NewProc("ReplaceFileW")
)

func replaceConfigTarget(source, target, backup string) error {
	if _, err := os.Stat(target); os.IsNotExist(err) {
		return os.Rename(source, target)
	}
	sourcePtr, err := syscall.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	backupPtr, err := syscall.UTF16PtrFromString(backup)
	if err != nil {
		return err
	}
	targetPtr, err := syscall.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	// ReplaceFile replaces the existing target without a direct truncating
	// write. The prior target is held in the explicit backup managed by the
	// caller, so the prior config remains recoverable until the replacement
	// has succeeded and the caller removes the backup.
	r1, _, callErr := procReplaceFileW.Call(
		uintptr(unsafe.Pointer(targetPtr)),
		uintptr(unsafe.Pointer(sourcePtr)),
		uintptr(unsafe.Pointer(backupPtr)),
		1, // REPLACEFILE_WRITE_THROUGH
		0,
		0,
	)
	if r1 == 0 {
		if callErr != syscall.Errno(0) {
			return callErr
		}
		return syscall.EINVAL
	}
	return nil
}
