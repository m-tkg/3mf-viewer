// Package scan は指定パス配下のモデルファイル (.3mf / .stl) の探索と、
// 配信してよいパスの検証 (パストラバーサル対策) を担う。
package scan

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// ErrNotAllowed は配信対象外のパスが要求されたときのエラー。
var ErrNotAllowed = errors.New("path not allowed")

// File は探索で見つかったモデルファイル。
type File struct {
	// Path はルートからの相対パス (スラッシュ区切り)。API の識別子として使う。
	Path string `json:"path"`
	// Name はファイル名。
	Name string `json:"name"`
	// Dir はルートからの相対ディレクトリ ("" ならルート直下)。
	Dir string `json:"dir"`
	// AbsPath は絶対パス (クリップボードコピー用)。
	AbsPath string `json:"absPath"`
	Size    int64  `json:"size"`
	// ModTime は更新時刻 (Unix ミリ秒)。
	ModTime int64 `json:"modTime"`
	// Kind は "3mf" または "stl"。
	Kind string `json:"kind"`
}

// Kind は拡張子からモデル種別を返す。対象外なら "" を返す。
func Kind(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".3mf":
		return "3mf"
	case ".stl":
		return "stl"
	}
	return ""
}

// Root は閲覧対象のルート。ルートはディレクトリ、または単一ファイル。
type Root struct {
	// Dir はシンボリックリンク解決済みの絶対ディレクトリ。
	Dir string
	// single は単一ファイル指定のときのファイル名 (それ以外は "")。
	single string
	// target は単一ファイル指定のときの、シンボリックリンク解決後の実ファイルパス。
	target string
	// display はユーザーが指定した (リンク未解決の) 絶対ディレクトリ。AbsPath の表示に使う。
	display string
}

// NewRoot は p (ディレクトリまたは .3mf/.stl ファイル) からルートを作る。
func NewRoot(p string) (*Root, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	r := &Root{}
	if st.IsDir() {
		r.display = abs
	} else {
		if Kind(abs) == "" {
			return nil, fmt.Errorf("%s: .3mf / .stl 以外のファイルは指定できない", p)
		}
		r.display = filepath.Dir(abs)
		r.single = filepath.Base(abs)
		// 指定ファイル自体がシンボリックリンクでも読めるよう、ファイル単位で解決しておく。
		r.target, err = filepath.EvalSymlinks(abs)
		if err != nil {
			return nil, err
		}
	}
	r.Dir, err = filepath.EvalSymlinks(r.display)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// hidden は "." で始まる (隠し) 名前か。macOS の "._foo.stl" 等を除外するために使う。
func hidden(name string) bool {
	return strings.HasPrefix(name, ".")
}

// inside は resolved (リンク解決済み絶対パス) がルート配下かを返す。
func (r *Root) inside(resolved string) bool {
	if r.single != "" {
		return resolved == r.target
	}
	return resolved == r.Dir || strings.HasPrefix(resolved, r.Dir+string(filepath.Separator))
}

// List はルート配下のモデルファイルを再帰的に列挙する (パス順)。
func (r *Root) List() ([]File, error) {
	var files []File
	if r.single != "" {
		f, ok := r.entry(r.single)
		if ok {
			files = append(files, f)
		}
		return files, nil
	}
	err := filepath.WalkDir(r.display, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == r.display {
				return err
			}
			// 読めないディレクトリ・競合で消えたファイルは無視して続行する。
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		name := d.Name()
		if p != r.display && hidden(name) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() || Kind(name) == "" {
			return nil
		}
		rel, err := filepath.Rel(r.display, p)
		if err != nil {
			return nil
		}
		if f, ok := r.entry(filepath.ToSlash(rel)); ok {
			files = append(files, f)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool {
		return strings.ToLower(files[i].Path) < strings.ToLower(files[j].Path)
	})
	return files, nil
}

// entry は rel のファイル情報を作る。配信不可 (リンク先がルート外など) なら ok=false。
func (r *Root) entry(rel string) (File, bool) {
	abs, err := r.Resolve(rel)
	if err != nil {
		return File{}, false
	}
	st, err := os.Stat(abs)
	if err != nil || !st.Mode().IsRegular() {
		return File{}, false
	}
	dir := path.Dir(rel)
	if dir == "." {
		dir = ""
	}
	return File{
		Path:    rel,
		Name:    path.Base(rel),
		Dir:     dir,
		AbsPath: filepath.Join(r.display, filepath.FromSlash(rel)),
		Size:    st.Size(),
		ModTime: st.ModTime().UnixMilli(),
		Kind:    Kind(rel),
	}, true
}

// Resolve は API から渡された相対パス rel を、読み込み可能な実ファイルのパスに解決する。
// ".." による脱出、隠しファイル、対象外の拡張子、ルート外を指すシンボリックリンクは
// ErrNotAllowed になる。
func (r *Root) Resolve(rel string) (string, error) {
	if rel == "" || strings.ContainsRune(rel, 0) {
		return "", ErrNotAllowed
	}
	if filepath.Separator != '/' && strings.ContainsRune(rel, filepath.Separator) {
		return "", ErrNotAllowed
	}
	// ".." を含むパスは正規化で吸収せず、明示的に拒否する。
	for _, seg := range strings.Split(rel, "/") {
		if seg == ".." || (seg != "" && hidden(seg)) {
			return "", ErrNotAllowed
		}
	}
	clean := path.Clean("/" + rel)
	if clean == "/" || Kind(clean) == "" {
		return "", ErrNotAllowed
	}
	full := filepath.Join(r.display, filepath.FromSlash(clean))
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		return "", err
	}
	if !r.inside(resolved) {
		return "", ErrNotAllowed
	}
	return resolved, nil
}

// Change はファイル一覧の差分。
type Change struct {
	Added    []string `json:"added"`
	Modified []string `json:"modified"`
	Removed  []string `json:"removed"`
}

// Empty は差分が無いか。
func (c Change) Empty() bool {
	return len(c.Added) == 0 && len(c.Modified) == 0 && len(c.Removed) == 0
}

// Diff は old から cur への差分を返す。
func Diff(old, cur []File) Change {
	c := Change{Added: []string{}, Modified: []string{}, Removed: []string{}}
	om := make(map[string]File, len(old))
	for _, f := range old {
		om[f.Path] = f
	}
	seen := make(map[string]bool, len(cur))
	for _, f := range cur {
		seen[f.Path] = true
		o, ok := om[f.Path]
		switch {
		case !ok:
			c.Added = append(c.Added, f.Path)
		case o.Size != f.Size || o.ModTime != f.ModTime:
			c.Modified = append(c.Modified, f.Path)
		}
	}
	for _, f := range old {
		if !seen[f.Path] {
			c.Removed = append(c.Removed, f.Path)
		}
	}
	return c
}
