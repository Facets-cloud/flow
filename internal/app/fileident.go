package app

import (
	"os"
	"syscall"
)

// fileInode returns the inode number of fi, or 0 when the platform does
// not expose one. Combined with size it identifies a source transcript so
// capture can detect rotation (a new file reusing the same path). Compiles
// on darwin and linux (flow's supported hosts); a 0 fallback keeps the
// capture correct elsewhere by leaning on size+path alone.
func fileInode(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino)
	}
	return 0
}
