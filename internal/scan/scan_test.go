package scan

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func touch(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func paths(files []File) []string {
	var out []string
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

func TestListRecursiveAndFilter(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "a.3mf"))
	touch(t, filepath.Join(dir, "B.STL"))
	touch(t, filepath.Join(dir, "sub", "deep", "c.stl"))
	touch(t, filepath.Join(dir, "notes.txt"))
	touch(t, filepath.Join(dir, ".hidden", "d.stl"))
	touch(t, filepath.Join(dir, "._a.stl"))

	r, err := NewRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	files, err := r.List()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a.3mf", "B.STL", "sub/deep/c.stl"}
	if got := paths(files); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	c := files[2]
	if c.Name != "c.stl" || c.Dir != "sub/deep" || c.Kind != "stl" || c.Size != 1 {
		t.Fatalf("unexpected entry: %+v", c)
	}
	if !filepath.IsAbs(c.AbsPath) {
		t.Fatalf("AbsPath not absolute: %s", c.AbsPath)
	}
	if files[1].Kind != "stl" {
		t.Fatalf("uppercase ext kind = %q", files[1].Kind)
	}
}

func TestSingleFileRoot(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "a.stl"))
	touch(t, filepath.Join(dir, "b.stl"))
	r, err := NewRoot(filepath.Join(dir, "a.stl"))
	if err != nil {
		t.Fatal(err)
	}
	files, _ := r.List()
	if got := paths(files); !reflect.DeepEqual(got, []string{"a.stl"}) {
		t.Fatalf("got %v", got)
	}
	if _, err := r.Resolve("b.stl"); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("sibling must be rejected, got %v", err)
	}
	if _, err := r.Resolve("a.stl"); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRoot(filepath.Join(dir, "missing.stl")); err == nil {
		t.Fatal("missing path should fail")
	}
}

func TestNewRootRejectsOtherFile(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "x.txt"))
	if _, err := NewRoot(filepath.Join(dir, "x.txt")); err == nil {
		t.Fatal("non-model file should be rejected")
	}
}

func TestResolveTraversal(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	touch(t, filepath.Join(root, "ok.stl"))
	touch(t, filepath.Join(base, "secret.stl")) // ルート外
	touch(t, filepath.Join(root, ".hid.stl"))
	touch(t, filepath.Join(root, "note.txt"))

	r, err := NewRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve("ok.stl"); err != nil {
		t.Fatalf("valid path rejected: %v", err)
	}
	for _, bad := range []string{
		"", "../secret.stl", "sub/../../secret.stl", "..", "/../secret.stl",
		"..%2fsecret.stl", ".hid.stl", "note.txt", "ok.stl\x00.txt", "/",
	} {
		_, err := r.Resolve(bad)
		if err == nil {
			t.Errorf("Resolve(%q) should fail", bad)
		}
	}
	// 絶対パス風の入力はルート配下の相対として扱われ、ルート外には出ない。
	if p, err := r.Resolve(filepath.Join(base, "secret.stl")); err == nil {
		t.Errorf("absolute path resolved to %s", p)
	}
}

func TestSymlinkEscape(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	touch(t, filepath.Join(root, "ok.stl"))
	touch(t, filepath.Join(base, "outside", "out.stl"))
	if err := os.Symlink(filepath.Join(base, "outside", "out.stl"), filepath.Join(root, "link.stl")); err != nil {
		t.Skip("symlink unsupported:", err)
	}
	if err := os.Symlink(filepath.Join(root, "ok.stl"), filepath.Join(root, "inner.stl")); err != nil {
		t.Fatal(err)
	}

	r, _ := NewRoot(root)
	if _, err := r.Resolve("link.stl"); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("escaping symlink must be rejected, got %v", err)
	}
	if _, err := r.Resolve("inner.stl"); err != nil {
		t.Fatalf("inner symlink should be allowed: %v", err)
	}
	files, _ := r.List()
	if got := paths(files); !reflect.DeepEqual(got, []string{"inner.stl", "ok.stl"}) {
		t.Fatalf("list = %v", got)
	}
}

func TestDiff(t *testing.T) {
	old := []File{{Path: "a", Size: 1, ModTime: 1}, {Path: "b", Size: 1, ModTime: 1}, {Path: "c", Size: 1, ModTime: 1}}
	cur := []File{{Path: "a", Size: 1, ModTime: 1}, {Path: "b", Size: 2, ModTime: 1}, {Path: "d", Size: 1, ModTime: 1}}
	c := Diff(old, cur)
	if !reflect.DeepEqual(c.Added, []string{"d"}) || !reflect.DeepEqual(c.Modified, []string{"b"}) || !reflect.DeepEqual(c.Removed, []string{"c"}) {
		t.Fatalf("diff = %+v", c)
	}
	if !Diff(cur, cur).Empty() {
		t.Fatal("same list should give empty diff")
	}
}

func TestSingleFileSymlinkRoot(t *testing.T) {
	base := t.TempDir()
	touch(t, filepath.Join(base, "real", "model.stl"))
	touch(t, filepath.Join(base, "other.stl"))
	linkDir := filepath.Join(base, "links")
	if err := os.MkdirAll(linkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(linkDir, "link.stl")
	if err := os.Symlink(filepath.Join(base, "real", "model.stl"), link); err != nil {
		t.Skip("symlink unsupported:", err)
	}
	r, err := NewRoot(link)
	if err != nil {
		t.Fatal(err)
	}
	files, err := r.List()
	if err != nil {
		t.Fatal(err)
	}
	if got := paths(files); !reflect.DeepEqual(got, []string{"link.stl"}) {
		t.Fatalf("list = %v", got)
	}
	if _, err := r.Resolve("link.stl"); err != nil {
		t.Fatalf("symlinked single file must be readable: %v", err)
	}
	if _, err := r.Resolve("other.stl"); err == nil {
		t.Fatal("other files must stay unreachable")
	}
}
