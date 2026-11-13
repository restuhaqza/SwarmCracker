//go:build !unix

package agent

import "io/fs"

// fileUID reports the owner of a file from its stat information. Platforms
// without Unix ownership answer "unknown", so the caller keeps the
// permission-only check instead of refusing every token file.
func fileUID(fs.FileInfo) (uint32, bool) { return 0, false }

// fileOwner reports the owner and group of a file from its stat information.
// Platforms without Unix ownership answer "unknown", which the file operations
// report as zeros rather than inventing an owner.
func fileOwner(fs.FileInfo) (uid, gid int, ok bool) { return 0, 0, false }
