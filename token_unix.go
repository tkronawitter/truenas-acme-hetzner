//go:build unix

package main

import (
	"fmt"
	"os"
	"os/user"
	"strconv"
	"syscall"
)

// ownerWarnings reports a token file not owned by the effective user and group.
func ownerWarnings(path string, info os.FileInfo) []string {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	return ownerWarningsFor(path, st.Uid, st.Gid, uint32(os.Geteuid()), uint32(os.Getegid()))
}

// ownerWarningsFor compares the owner of the token file with the user and
// group tah runs as. The fix is chown/chgrp rather than "tah init", because
// init leaves the owner of an existing file as it is.
func ownerWarningsFor(path string, fileUID, fileGID, uid, gid uint32) []string {
	var warnings []string
	if fileUID != uid {
		name := strconv.FormatUint(uint64(uid), 10)
		if u, err := user.LookupId(name); err == nil {
			name = u.Username
		}
		warnings = append(warnings, fmt.Sprintf("Token file %s belongs to uid %d, but tah runs as uid %d. Run: sudo chown %s %s",
			path, fileUID, uid, name, path))
	}
	if fileGID != gid {
		name := strconv.FormatUint(uint64(gid), 10)
		if g, err := user.LookupGroupId(name); err == nil {
			name = g.Name
		}
		warnings = append(warnings, fmt.Sprintf("Token file %s belongs to gid %d, but tah runs as gid %d. Run: sudo chgrp %s %s",
			path, fileGID, gid, name, path))
	}
	return warnings
}
