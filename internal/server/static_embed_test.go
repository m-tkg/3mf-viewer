package server

import (
	"io/fs"
	"path"
	"regexp"
	"strings"
	"testing"
)

// go のモジュール zip は "vendor" という名前のディレクトリを除外する。
// go install 版で静的ファイルが欠落しないよう、static 配下に置かない。
func TestEmbeddedStaticHasNoVendorDir(t *testing.T) {
	err := fs.WalkDir(staticFS, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == "vendor" {
			t.Errorf("static 配下に vendor ディレクトリがある (モジュール zip から除外される): %s", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestEmbeddedStaticHasRequiredFiles(t *testing.T) {
	for _, p := range []string{
		"static/index.html", "static/app.js", "static/style.css",
		"static/lib/three.module.js", "static/lib/OrbitControls.js", "static/lib/LICENSE-three.txt",
	} {
		b, err := fs.ReadFile(staticFS, p)
		if err != nil || len(b) == 0 {
			t.Errorf("%s が埋め込まれていない: %v", p, err)
		}
	}
}

// static 配下の全 .js の import 指定子が相対パスで、埋め込み FS 上に解決できること。
// ベア指定子 ('three' 等) は import map が無く CSP も厳格なためブラウザで解決できない。
func TestStaticJSImportsResolve(t *testing.T) {
	re := regexp.MustCompile(`(?:\bfrom|\bimport)\s*['"]([^'"]+)['"]`)
	dyn := regexp.MustCompile(`\bimport\s*\(`)
	checked := 0
	err := fs.WalkDir(staticFS, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".js") {
			return err
		}
		b, err := fs.ReadFile(staticFS, p)
		if err != nil {
			return err
		}
		if dyn.Match(b) {
			t.Errorf("%s: 動的 import は検証対象外。テストを拡張すること", p)
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			spec := m[1]
			if !strings.HasPrefix(spec, "./") && !strings.HasPrefix(spec, "../") {
				t.Errorf("%s: 相対でない import 指定子 %q", p, spec)
				continue
			}
			target := path.Join(path.Dir(p), spec)
			if _, err := fs.Stat(staticFS, target); err != nil {
				t.Errorf("%s: import %q が埋め込みに無い (%s): %v", p, spec, target, err)
			}
			checked++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("import が 1 件も検査されていない")
	}
}
