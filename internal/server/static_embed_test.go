package server

import (
	"io/fs"
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

// app.js の相対 import が、すべて埋め込み FS に存在すること。
func TestAppJSImportsResolve(t *testing.T) {
	b, err := fs.ReadFile(staticFS, "static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`from\s+'(\./[^']+)'`)
	ms := re.FindAllStringSubmatch(string(b), -1)
	if len(ms) == 0 {
		t.Fatal("import が見つからない")
	}
	for _, m := range ms {
		p := "static/" + strings.TrimPrefix(m[1], "./")
		if _, err := fs.Stat(staticFS, p); err != nil {
			t.Errorf("import %s が埋め込みに無い: %v", m[1], err)
		}
	}
}
