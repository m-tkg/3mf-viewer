package scan

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	// ErrInvalidName は新しい名前が使えないとき (空、区切り文字、制御文字など)。
	ErrInvalidName = errors.New("invalid name")
	// ErrExists は変更先に同名のファイルが既にあるとき。上書きはしない。
	ErrExists = errors.New("同じ名前のファイルが既にある")
	// ErrSameName は変更前後の名前が同一のとき。
	ErrSameName = errors.New("名前が変わっていない")
	// ErrSingleRoot は単一ファイル指定のルートで名前変更しようとしたとき。
	ErrSingleRoot = errors.New("単一ファイルを指定して起動した場合は名前を変更できない")
)

// maxNameBytes は多くのファイルシステムのファイル名長の上限 (バイト)。
const maxNameBytes = 255

func invalidName(why string) error { return fmt.Errorf("%w: %s", ErrInvalidName, why) }

// validateBase は拡張子を除いた新しい名前 base を検証する。full は拡張子込みの最終的な名前。
func validateBase(base, full string) error {
	if strings.TrimSpace(base) == "" {
		return invalidName("名前が空")
	}
	if !utf8.ValidString(base) {
		return invalidName("不正な文字列")
	}
	if strings.ContainsAny(base, "/\\") {
		return invalidName("/ や \\ は使えない")
	}
	if base == ".." || strings.HasPrefix(base, ".") {
		return invalidName("先頭に . は使えない")
	}
	for _, c := range base {
		// 制御文字と、表示を偽装できる書式文字 (RLO など)・行区切りを拒否する。
		if unicode.IsControl(c) || unicode.Is(unicode.Cf, c) || c == ' ' || c == ' ' {
			return invalidName("制御文字は使えない")
		}
	}
	first, _ := utf8.DecodeRuneInString(base)
	last, _ := utf8.DecodeLastRuneInString(base)
	if unicode.IsSpace(first) || unicode.IsSpace(last) {
		return invalidName("前後に空白は使えない")
	}
	if len(full) > maxNameBytes {
		return invalidName("名前が長すぎる")
	}
	return nil
}

// Rename は rel のファイルを、同じディレクトリ内で拡張子を保ったまま base に改名する。
// 戻り値は新しい相対パス。変更先が存在する場合は上書きせず ErrExists を返す。
// 対象と変更先がルート配下にあること (シンボリックリンク解決後) も検証する。
func (r *Root) Rename(rel, base string) (string, error) {
	if r.single != "" {
		return "", ErrSingleRoot
	}
	// 対象が配信可能 (隠しでない・対象拡張子・ルート配下) であることを確認する。
	if _, err := r.Resolve(rel); err != nil {
		return "", err
	}
	clean := path.Clean("/" + rel)
	dirRel := path.Dir(clean)
	oldName := path.Base(clean)
	newName := base + path.Ext(oldName)
	if err := validateBase(base, newName); err != nil {
		return "", err
	}
	if newName == oldName {
		return "", ErrSameName
	}
	// 親ディレクトリを解決し、ルート配下であることを確認する (リンク経由の脱出を防ぐ)。
	dir, err := filepath.EvalSymlinks(filepath.Join(r.display, filepath.FromSlash(dirRel)))
	if err != nil {
		return "", err
	}
	if !r.inside(dir) {
		return "", ErrNotAllowed
	}
	src := filepath.Join(dir, oldName)
	dst := filepath.Join(dir, newName)
	if filepath.Dir(dst) != dir { // 念のため (名前に区切りが残っていないこと)
		return "", ErrNotAllowed
	}
	srcInfo, err := os.Lstat(src)
	if err != nil {
		return "", err
	}
	if !srcInfo.Mode().IsRegular() && srcInfo.Mode()&fs.ModeSymlink == 0 {
		return "", ErrNotAllowed
	}
	if dstInfo, err := os.Lstat(dst); err == nil {
		// dst に Lstat が通っても、ディレクトリに newName と完全一致するエントリが無く、かつ src と同一ファイルなら、
		// 大文字小文字・Unicode 正規化 (NFC/NFD) を区別しない FS が同じエントリを別表記で指しているだけ。
		// この場合は通常の rename で表記だけ変える。完全一致のエントリが既にあれば (区別する FS 上の
		// ハードリンクなど) 別エントリなので上書きしない。
		if os.SameFile(srcInfo, dstInfo) {
			exact, err := hasEntry(dir, newName)
			if err != nil {
				return "", err
			}
			if !exact {
				if err := os.Rename(src, dst); err != nil {
					return "", err
				}
				return strings.TrimPrefix(path.Join(dirRel, newName), "/"), nil
			}
		}
		return "", ErrExists
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if err := renameNoReplace(src, dst, srcInfo); err != nil {
		return "", err
	}
	return strings.TrimPrefix(path.Join(dirRel, newName), "/"), nil
}

// hasEntry は dir 直下に name と完全一致 (バイト列) するエントリがあるか。
func hasEntry(dir, name string) (bool, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	for _, e := range ents {
		if e.Name() == name {
			return true, nil
		}
	}
	return false, nil
}

// renameNoReplace は dst が存在しない場合に限り src を dst に移す。
// 確認と rename の間に dst が作られても上書きしないよう、link (既存なら EEXIST) + unlink を使う。
// シンボリックリンク自体は同じ内容のリンクを新規作成 (既存なら EEXIST) してから元を消す。
func renameNoReplace(src, dst string, srcInfo fs.FileInfo) error {
	if srcInfo.Mode()&fs.ModeSymlink != 0 {
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		if err := os.Symlink(target, dst); err != nil {
			if errors.Is(err, fs.ErrExist) {
				return ErrExists
			}
			return err
		}
		// リンク自体は inode が別になるので、リンク先の文字列が同じかで同一性を判断する。
		if cur, err := os.Readlink(src); err != nil || cur != target {
			// 作ったばかりの dst は片付ける (まだ自分が作ったリンクのときだけ)。
			if t, e := os.Readlink(dst); e == nil && t == target {
				_ = os.Remove(dst)
			}
			if err != nil && errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return errors.New("改名中に元のファイルが差し替えられたため、元の名前のファイルは残した")
		}
		return os.Remove(src)
	}
	if err := os.Link(src, dst); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return ErrExists
		}
		if errors.Is(err, fs.ErrNotExist) {
			return err
		}
		// ハードリンク非対応の FS (exFAT / ネットワーク FS など)。
		// 競合の窓は残るが、直前に存在を再確認したうえで通常の rename にフォールバックする。
		if _, e := os.Lstat(dst); e == nil {
			return ErrExists
		} else if !errors.Is(e, fs.ErrNotExist) {
			return e
		}
		return os.Rename(src, dst)
	}
	return removeIfSame(src, dst)
}

// removeIfSame は src を消す。直前に別ファイルへ差し替えられていた場合は消さずにエラーにする
// (その場合も改名後の dst には元のファイルが残る)。
func removeIfSame(src, dst string) error {
	si, err := os.Lstat(src)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	di, err := os.Lstat(dst)
	if err != nil {
		return err
	}
	if !os.SameFile(si, di) {
		return errors.New("改名中に元のファイルが差し替えられたため、元の名前のファイルは残した")
	}
	return os.Remove(src)
}
