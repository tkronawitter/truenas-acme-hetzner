//go:build !unix

package main

import "os"

// ownerWarnings has no uid/gid to compare outside unix. The release target is
// linux; this file exists so the tests also build and run on a Windows workstation.
func ownerWarnings(os.FileInfo) []string {
	return nil
}
