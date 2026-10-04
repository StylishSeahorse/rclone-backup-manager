package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/stylishseahorse/rclone-backup-manager/internal/proto"
)

// maxEntries bounds one listing so a directory with millions of files can't
// exhaust agent memory or produce a multi-hundred-MB WebSocket frame.
const maxEntries = 5000

// Scanner lists the local filesystem for the dashboard's file tree, but only
// beneath a fixed set of allowed roots (the directories mounted into the
// container, e.g. /host and /data). Anything outside is invisible, and
// symlinks are never followed out of the roots.
type Scanner struct {
	roots []string // cleaned absolute paths, as configured
	real  []string // the same roots after resolving symlinks
}

func NewScanner(roots []string) (*Scanner, error) {
	s := &Scanner{}
	for _, r := range roots {
		r = filepath.Clean(strings.TrimSpace(r))
		if !filepath.IsAbs(r) {
			return nil, fmt.Errorf("browse root %q must be absolute", r)
		}
		real, err := filepath.EvalSymlinks(r)
		if err != nil {
			continue // not mounted in this container: just skip it
		}
		s.roots = append(s.roots, r)
		s.real = append(s.real, real)
	}
	if len(s.roots) == 0 {
		return nil, errors.New("none of the configured browse roots exist; mount a host directory (e.g. -v /srv:/host/srv:ro) or set AGENT_BROWSE_ROOTS")
	}
	return s, nil
}

func (s *Scanner) Roots() []string { return append([]string(nil), s.roots...) }

// Resolve validates that p lies inside an allowed root and returns the cleaned
// path. Symlinks anywhere in p are resolved first, so "/host/link-to-etc"
// pointing outside the roots is rejected rather than followed.
func (s *Scanner) Resolve(p string) (string, error) {
	if strings.ContainsRune(p, 0) {
		return "", errors.New("invalid path")
	}
	if !filepath.IsAbs(p) {
		return "", errors.New("path must be absolute")
	}
	clean := filepath.Clean(p)
	real, err := filepath.EvalSymlinks(clean)
	if err != nil {
		if os.IsNotExist(err) {
			return "", errors.New("no such file or directory")
		}
		return "", err
	}
	for i := range s.roots {
		if within(real, s.real[i]) {
			return clean, nil
		}
	}
	return "", errors.New("path is outside the allowed backup roots")
}

// within reports whether child equals or is nested under parent (component-wise,
// so /hostile is not "within" /host).
func within(child, parent string) bool {
	if child == parent {
		return true
	}
	if !strings.HasSuffix(parent, string(filepath.Separator)) {
		parent += string(filepath.Separator)
	}
	return strings.HasPrefix(child, parent)
}

// Browse returns the directory listing for p. An empty path or "/" returns the
// virtual top level: the list of allowed roots.
func (s *Scanner) Browse(p string) (*proto.BrowseResult, error) {
	if p == "" || p == "/" {
		res := &proto.BrowseResult{Path: "/", Entries: []proto.Entry{}}
		for _, r := range s.roots {
			fi, err := os.Stat(r)
			if err != nil {
				continue
			}
			res.Entries = append(res.Entries, entryFrom(r, filepath.Base(r), fi, false))
		}
		return res, nil
	}

	clean, err := s.Resolve(p)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(clean)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, errors.New("not a directory")
	}
	dirents, err := os.ReadDir(clean)
	if err != nil {
		return nil, err
	}

	res := &proto.BrowseResult{Path: clean, Parent: s.parentOf(clean), Entries: make([]proto.Entry, 0, len(dirents))}
	for _, de := range dirents {
		if len(res.Entries) >= maxEntries {
			res.Truncated = true
			break
		}
		full := filepath.Join(clean, de.Name())
		// Lstat, not Stat: a symlink is reported as a symlink (never as a
		// directory), so the tree can't be walked out through one.
		info, err := os.Lstat(full)
		if err != nil {
			continue // vanished or unreadable: skip, don't fail the listing
		}
		res.Entries = append(res.Entries, entryFrom(full, de.Name(), info, info.Mode()&os.ModeSymlink != 0))
	}
	// Directories first, then case-insensitive by name.
	sort.Slice(res.Entries, func(i, j int) bool {
		a, b := res.Entries[i], res.Entries[j]
		if a.IsDir != b.IsDir {
			return a.IsDir
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	return res, nil
}

// parentOf returns the parent directory, or "/" (the virtual top level) when
// clean is itself an allowed root.
func (s *Scanner) parentOf(clean string) string {
	for _, r := range s.roots {
		if clean == r {
			return "/"
		}
	}
	return filepath.Dir(clean)
}

func entryFrom(full, name string, fi os.FileInfo, symlink bool) proto.Entry {
	e := proto.Entry{
		Name:    name,
		Path:    full,
		IsDir:   fi.IsDir() && !symlink,
		ModTime: fi.ModTime().Unix(),
		Mode:    fi.Mode().String(),
		Symlink: symlink,
	}
	if !e.IsDir {
		e.Size = fi.Size()
	}
	return e
}
