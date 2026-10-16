//go:build unix

package main

import (
	"fmt"
	"os"
	"syscall"
)

// ownerWarnings reports a token file not owned by the effective user and group.
func ownerWarnings(info os.FileInfo) []string {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}

	var warnings []string
	if st.Uid != uint32(os.Geteuid()) {
		warnings = append(warnings, fmt.Sprintf("Different user for config file: %d/%d", st.Uid, os.Geteuid()))
	}
	if st.Gid != uint32(os.Getegid()) {
		warnings = append(warnings, fmt.Sprintf("Different group for config file: %d/%d", st.Gid, os.Getegid()))
	}
	return warnings
}
