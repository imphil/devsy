//go:build windows

package vscode

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// ideBreakawayProcAttr starts the editor CLI outside the caller's job object,
// so the IDE it opens outlives a detached task worker (see runHostCLI).
func ideBreakawayProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: windows.CREATE_BREAKAWAY_FROM_JOB}
}
