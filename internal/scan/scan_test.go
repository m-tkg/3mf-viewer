package scan

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

func TestRename(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "root")
	touch(t, filepath.Join(dir, "sub", "a.STL"))
	touch(t, filepath.Join(dir, "b.3mf"))
	touch(t, filepath.Join(dir, "taken.3mf"))
	touch(t, filepath.Join(base, "outside.stl"))
	r, err := NewRoot(dir)
	if err != nil {
		t.Fatal(err)
	}

	// 正常: 拡張子 (大文字小文字含む) と親ディレクトリは保たれる。
	got, err := r.Rename("sub/a.STL", "新しい 名前")
	if err != nil || got != "sub/新しい 名前.STL" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sub", "新しい 名前.STL")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "sub", "a.STL")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old name must be gone: %v", err)
	}

	// 大文字小文字だけの変更は成功する。
	if got, err := r.Rename("b.3mf", "B"); err != nil || got != "B.3mf" {
		t.Fatalf("case rename: %q %v", got, err)
	}

	// 上書き拒否 (どちらのファイルも残る)。
	if _, err := r.Rename("B.3mf", "taken"); !errors.Is(err, ErrExists) {
		t.Fatalf("want ErrExists, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "B.3mf")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Rename("B.3mf", "B"); !errors.Is(err, ErrSameName) {
		t.Fatalf("want ErrSameName, got %v", err)
	}

	// 不正な名前。
	long := strings.Repeat("あ", 90) // 270 バイト
	for _, n := range []string{"", "  ", "a/b", `a\b`, "..", ".hidden", "a\x00b", "a\nb", "a\x7fb", "a‮b", " lead", "trail ", long, "../x"} {
		if _, err := r.Rename("B.3mf", n); !errors.Is(err, ErrInvalidName) {
			t.Errorf("name %q: want ErrInvalidName, got %v", n, err)
		}
	}

	// 対象パスの不正: 配下外・隠し・対象外拡張子・存在しない。
	for _, p := range []string{"../outside.stl", "sub/../../outside.stl", "", ".x.stl", "taken.txt"} {
		if _, err := r.Rename(p, "z"); !errors.Is(err, ErrNotAllowed) {
			t.Errorf("path %q: want ErrNotAllowed, got %v", p, err)
		}
	}
	if _, err := r.Rename("nope.stl", "z"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "outside.stl")); err != nil {
		t.Fatal(err)
	}
}

func TestRenameSymlinks(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "root")
	touch(t, filepath.Join(dir, "real.stl"))
	touch(t, filepath.Join(base, "outside", "o.stl"))
	if err := os.Symlink(filepath.Join(base, "outside"), filepath.Join(dir, "escape")); err != nil {
		t.Skip("symlink unsupported:", err)
	}
	if err := os.Symlink(filepath.Join(base, "outside", "o.stl"), filepath.Join(dir, "out.stl")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.stl", filepath.Join(dir, "link.stl")); err != nil {
		t.Fatal(err)
	}
	r, err := NewRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	// 配下外へ出るディレクトリリンク / ファイルリンクは拒否。
	if _, err := r.Rename("escape/o.stl", "x"); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("escape dir: %v", err)
	}
	if _, err := r.Rename("out.stl", "x"); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("escape file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "outside", "o.stl")); err != nil {
		t.Fatal(err)
	}
	// 配下内を指すリンクは、リンク自体を改名する (実体は動かない)。
	if got, err := r.Rename("link.stl", "renamed"); err != nil || got != "renamed.stl" {
		t.Fatalf("%q %v", got, err)
	}
	if tgt, err := os.Readlink(filepath.Join(dir, "renamed.stl")); err != nil || tgt != "real.stl" {
		t.Fatalf("link target %q %v", tgt, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "real.stl")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "link.stl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old link must be gone: %v", err)
	}
	// リンクの改名でも上書きしない。
	if _, err := r.Rename("renamed.stl", "real"); !errors.Is(err, ErrExists) {
		t.Fatalf("link overwrite: %v", err)
	}
}

func TestRenameSingleRoot(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "one.stl"))
	r, err := NewRoot(filepath.Join(dir, "one.stl"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Rename("one.stl", "two"); !errors.Is(err, ErrSingleRoot) {
		t.Fatalf("got %v", err)
	}
}

// 変更先が確認後に作られても上書きしない (link が EEXIST になる) ことの確認。
func TestRenameNoReplaceRace(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "a.stl"))
	if err := os.WriteFile(filepath.Join(dir, "b.stl"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	si, err := os.Lstat(filepath.Join(dir, "a.stl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := renameNoReplace(filepath.Join(dir, "a.stl"), filepath.Join(dir, "b.stl"), si); !errors.Is(err, ErrExists) {
		t.Fatalf("got %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "b.stl")); string(b) != "keep" {
		t.Fatalf("destination overwritten: %q", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.stl")); err != nil {
		t.Fatal(err)
	}
}

// 大文字小文字を区別する FS で、同じ inode を指す a.stl / A.stl の別エントリがあるとき、上書きしない。
func TestRenameDoesNotOverwriteCaseVariantHardlink(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "a.stl"))
	if err := os.Link(filepath.Join(dir, "a.stl"), filepath.Join(dir, "A.stl")); err != nil {
		t.Skip("hard link unsupported:", err)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) < 2 {
		t.Skip("case-insensitive filesystem")
	}
	r, err := NewRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Rename("a.stl", "A"); !errors.Is(err, ErrExists) {
		t.Fatalf("want ErrExists, got %v", err)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 2 {
		t.Fatalf("entries changed: %v", ents)
	}
}

// NFD で保存された名前を、NFC の同じ名前 (ブラウザ入力) で改名しても、自分自身との衝突にならない。
func TestRenameNormalizationVariant(t *testing.T) {
	dir := t.TempDir()
	nfd := "\u304b\u3099" // か + 結合濁点
	touch(t, filepath.Join(dir, nfd+".stl"))
	r, err := NewRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Rename(nfd+".stl", "\u304c"); err != nil && !errors.Is(err, ErrSameName) {
		t.Fatalf("got %v", err)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Fatalf("entries: %v", ents)
	}
}
