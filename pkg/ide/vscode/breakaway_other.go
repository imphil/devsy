//go:build !windows

package vscode

import "syscall"

// ideBreakawayProcAttr is Windows-only (job objects); nil elsewhere.
func ideBreakawayProcAttr() *syscall.SysProcAttr { return nil }
