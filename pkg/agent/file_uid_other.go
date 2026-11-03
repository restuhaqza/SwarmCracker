//go:build !unix

package agent

import "io/fs"

// fileUID reports the owner of a file from its stat information. Platforms
// without Unix ownership answer "unknown", so the caller keeps the
// permission-only check instead of refusing every token file.
func fileUID(fs.FileInfo) (uint32, bool) { return 0, false }
