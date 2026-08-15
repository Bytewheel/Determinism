// Package cache provides compressed filesystem/repository caching so that
// prompts which need pre-existing data can be replayed deterministically.
//
// A cache entry is a gzip-compressed tarball of a directory tree, keyed by a
// stable hash of (session id, workdir, prompt). Restoring an entry replaces
// the target directory contents with the captured state.
package cache

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Cache is a directory of compressed snapshots.
type Cache struct {
	Dir string
}

// New creates a Cache rooted at dir, creating it if needed.
func New(dir string) (*Cache, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Cache{Dir: dir}, nil
}

// Key computes a stable cache key for a session+workdir+prompt triple.
func Key(sessionID, workdir, prompt string) string {
	h := sha256.New()
	h.Write([]byte(sessionID))
	h.Write([]byte{0})
	h.Write([]byte(workdir))
	h.Write([]byte{0})
	h.Write([]byte(prompt))
	return hex.EncodeToString(h.Sum(nil))[:32]
}

func (c *Cache) archivePath(key string) string {
	return filepath.Join(c.Dir, key+".tar.gz")
}

// Exists reports whether a key has a snapshot.
func (c *Cache) Exists(key string) bool {
	_, err := os.Stat(c.archivePath(key))
	return err == nil
}

// Save compresses the directory tree at src into a snapshot. It excludes the
// cache directory itself to avoid recursion, and skips .git by default when
// skipGit is true (repositories are typically large and their content is
// reproducible from the commands being replayed).
func (c *Cache) Save(key, src string, skipGit bool) error {
	abs, err := filepath.Abs(src)
	if err != nil {
		return err
	}
	tmp := c.archivePath(key) + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	defer func() {
		f.Close()
		os.Remove(tmp)
	}()

	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	if err := writeTree(tw, abs, c.Dir, skipGit); err != nil {
		tw.Close()
		gz.Close()
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, c.archivePath(key))
}

func writeTree(tw *tar.Writer, root, skipDir string, skipGit bool) error {
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if skipDir != "" && strings.HasPrefix(path, skipDir) && path != root {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if skipGit && d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if !d.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(files)
	for _, f := range files {
		rel, err := filepath.Rel(root, f)
		if err != nil {
			return err
		}
		info, err := os.Lstat(f)
		if err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		hdr.ModTime = time.Unix(0, 0)
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		fh, err := os.Open(f)
		if err != nil {
			return err
		}
		if _, err := io.Copy(tw, fh); err != nil {
			fh.Close()
			return err
		}
		fh.Close()
	}
	return nil
}

// Restore extracts a snapshot into dst, overwriting existing files. Existing
// files that are not present in the archive are removed, restoring the exact
// captured state.
func (c *Cache) Restore(key, dst string) error {
	f, err := os.Open(c.archivePath(key))
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	// First pass: extract all files.
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		rel := filepath.FromSlash(hdr.Name)
		if filepath.IsAbs(rel) || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("unsafe path in archive: %s", hdr.Name)
		}
		target := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return err
		}
		out.Close()
	}
	return pruneAbsent(dst, c.archivePath(key))
}

// pruneAbsent removes files in dst that are not recorded in the archive.
func pruneAbsent(dst, archive string) error {
	// Re-open and collect set of archived paths.
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	archived := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag == tar.TypeReg {
			archived[filepath.FromSlash(hdr.Name)] = true
		}
	}
	return filepath.WalkDir(dst, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == dst {
			return nil
		}
		rel, rerr := filepath.Rel(dst, path)
		if rerr != nil {
			return nil
		}
		if d.IsDir() {
			// prune dirs only if we are sure none of its contents are archived
			return nil
		}
		if !archived[rel] {
			return os.Remove(path)
		}
		return nil
	})
}

// List returns entries in the cache with size and modification time.
func (c *Cache) List() ([]Entry, error) {
	entries, err := os.ReadDir(c.Dir)
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".tar.gz") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, Entry{
			Key:      strings.TrimSuffix(e.Name(), ".tar.gz"),
			Size:     info.Size(),
			Modified: info.ModTime(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out, nil
}

type Entry struct {
	Key      string
	Size     int64
	Modified time.Time
}

// Info returns metadata about a single entry.
func (c *Cache) Info(key string) (*Entry, error) {
	info, err := os.Stat(c.archivePath(key))
	if err != nil {
		return nil, err
	}
	return &Entry{Key: key, Size: info.Size(), Modified: info.ModTime()}, nil
}
