package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// fixture: <tmp>/root/{docs/a.txt, Zeta, alpha/, link-out -> /etc, link-in -> docs}, <tmp>/root-evil/
func fixture(t *testing.T) (root string, s *Scanner) {
	t.Helper()
	tmp := t.TempDir()
	tmp, _ = filepath.EvalSymlinks(tmp) // macOS-style /var -> /private/var safety
	root = filepath.Join(tmp, "root")
	for _, d := range []string{"docs", "alpha", "../root-evil"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(root, "docs", "a.txt"), []byte("hello"), 0o644))
	must(os.WriteFile(filepath.Join(root, "Zeta"), []byte("z"), 0o644))
	must(os.Symlink("/etc", filepath.Join(root, "link-out")))
	must(os.Symlink("docs", filepath.Join(root, "link-in")))
	s, err := NewScanner([]string{root, filepath.Join(tmp, "not-mounted")})
	must(err)
	return root, s
}

func TestResolveRejectsEscapes(t *testing.T) {
	root, s := fixture(t)
	bad := map[string]string{
		"outside root":     "/etc",
		"sibling prefix":   root + "-evil",
		"dotdot":           root + "/../root-evil",
		"dotdot to etc":    root + "/../../../../../../../../etc",
		"symlink escape":   root + "/link-out",
		"symlink escape 2": root + "/link-out/passwd",
		"relative":         "docs",
		"nul":              root + "/docs\x00/..",
		"missing":          root + "/nope",
	}
	for name, p := range bad {
		if got, err := s.Resolve(p); err == nil {
			t.Errorf("%s: Resolve(%q) = %q, want error", name, p, got)
		}
	}
	for _, p := range []string{root, root + "/docs", root + "/docs/a.txt", root + "/link-in", root + "/docs/../alpha"} {
		if _, err := s.Resolve(p); err != nil {
			t.Errorf("Resolve(%q): unexpected error %v", p, err)
		}
	}
}

func TestBrowseTopLevelListsOnlyMountedRoots(t *testing.T) {
	root, s := fixture(t)
	res, err := s.Browse("/")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 1 || res.Entries[0].Path != root || !res.Entries[0].IsDir {
		t.Fatalf("top level = %+v, want only %s", res.Entries, root)
	}
}

func TestBrowseSortsAndFlagsSymlinks(t *testing.T) {
	root, s := fixture(t)
	res, err := s.Browse(root)
	if err != nil {
		t.Fatal(err)
	}
	if res.Parent != "/" {
		t.Errorf("parent of a root = %q, want virtual top level \"/\"", res.Parent)
	}
	var names []string
	for _, e := range res.Entries {
		names = append(names, e.Name)
		if e.Symlink && e.IsDir {
			t.Errorf("symlink %s reported as a directory; the UI could walk out through it", e.Name)
		}
	}
	want := []string{"alpha", "docs", "link-in", "link-out", "Zeta"} // dirs first, then case-insensitive
	if fmt.Sprint(names) != fmt.Sprint(want) {
		t.Errorf("order = %v, want %v", names, want)
	}
	sub, err := s.Browse(filepath.Join(root, "docs"))
	if err != nil {
		t.Fatal(err)
	}
	if sub.Parent != root || len(sub.Entries) != 1 || sub.Entries[0].Size != 5 {
		t.Errorf("docs listing = %+v", sub)
	}
}

func TestBrowseErrors(t *testing.T) {
	root, s := fixture(t)
	if _, err := s.Browse(filepath.Join(root, "docs", "a.txt")); err == nil {
		t.Error("browsing a file should fail")
	}
	if _, err := s.Browse("/etc"); err == nil {
		t.Error("browsing outside roots should fail")
	}
	if _, err := NewScanner([]string{"/definitely/not/here"}); err == nil {
		t.Error("scanner with no existing roots should fail loudly")
	}
}

func TestBrowseTruncates(t *testing.T) {
	root, s := fixture(t)
	big := filepath.Join(root, "big")
	_ = os.Mkdir(big, 0o755)
	for i := 0; i < maxEntries+10; i++ {
		f, _ := os.Create(filepath.Join(big, fmt.Sprintf("f%05d", i)))
		f.Close()
	}
	res, err := s.Browse(big)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated || len(res.Entries) != maxEntries {
		t.Errorf("truncated=%v len=%d, want true/%d", res.Truncated, len(res.Entries), maxEntries)
	}
}
