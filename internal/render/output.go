package render

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// Output is a single file the pipeline will emit under PublicDir.
// Path is forward-slash, relative to PublicDir (no leading slash).
type Output struct {
	Path string
	Body []byte
}

// atomicWrite writes body to absPath via temp+rename so a reader can
// never observe a half-written file. The temp file lives in the same
// directory as the destination so the rename is on the same filesystem
// (rename across filesystems isn't atomic on POSIX).
//
// This function is load-bearing for the FileServer-vs-rebuild story:
// POSIX rename(2) is an atomic inode-table swap. An http.ServeFile
// reader that opened the previous file before the rename keeps reading
// the old inode's bytes until EOF; new requests open the new file.
// No reader can ever observe torn content.
func atomicWrite(absPath string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return err
	}
	var rnd [6]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return err
	}
	tmp := absPath + ".tmp-" + hex.EncodeToString(rnd[:])
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	// fsync the file before rename so a crash during the rename can't
	// leave a renamed-but-empty file. The rename itself is journaled by
	// the FS; the file's bytes need an explicit flush.
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, absPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename %s: %w", absPath, err)
	}
	return nil
}
