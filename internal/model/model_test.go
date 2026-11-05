package model

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// ---------- STL ----------

func binarySTL(tris [][9]float32, header string) []byte {
	var b bytes.Buffer
	h := make([]byte, 80)
	copy(h, header)
	b.Write(h)
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(tris)))
	for _, t := range tris {
		_ = binary.Write(&b, binary.LittleEndian, [3]float32{}) // normal
		_ = binary.Write(&b, binary.LittleEndian, t)
		b.Write([]byte{0, 0})
	}
	return b.Bytes()
}

var quad = [][9]float32{
	{0, 0, 0, 1, 0, 0, 1, 1, 0},
	{0, 0, 0, 1, 1, 0, 0, 1, 0},
}

func TestParseBinarySTLWelds(t *testing.T) {
	m, err := ParseSTL(binarySTL(quad, "binary"))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Indices) != 6 || len(m.Vertices) != 4*3 {
		t.Fatalf("indices=%d vertices=%d (want 6 / 12: 共有頂点が溶接される)", len(m.Indices), len(m.Vertices))
	}
}

func TestParseBinarySTLStartingWithSolid(t *testing.T) {
	// ヘッダが "solid" で始まるバイナリ STL を ASCII と誤判定しない。
	m, err := ParseSTL(binarySTL(quad, "solid exported by something"))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Indices) != 6 {
		t.Fatalf("indices=%d", len(m.Indices))
	}
}

func TestParseASCIISTL(t *testing.T) {
	src := `solid t
 facet normal 0 0 1
  outer loop
   vertex 0 0 0
   vertex 1 0 0
   vertex 0 1 0
  endloop
 endfacet
 facet normal 0 0 1
  outer loop
   vertex 1 0 0
   vertex 1 1 1e0
   vertex 0 1 0
  endloop
 endfacet
endsolid t
`
	m, err := ParseSTL([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Indices) != 6 || len(m.Vertices) != 4*3 {
		t.Fatalf("indices=%d vertices=%d", len(m.Indices), len(m.Vertices))
	}
}

func TestParseSTLErrors(t *testing.T) {
	if _, err := ParseSTL([]byte("")); err == nil {
		t.Error("empty should fail")
	}
	if _, err := ParseSTL([]byte("solid x\nendsolid x\n")); err == nil {
		t.Error("no triangles should fail")
	}
	nan := float32(math.NaN())
	if _, err := ParseSTL(binarySTL([][9]float32{{nan, 0, 0, 1, 0, 0, 1, 1, 0}}, "x")); err == nil {
		t.Error("only invalid triangles should fail")
	}
	// 三角形数がヘッダ申告より少ない (切り詰め) 場合は読めた分だけ返す。
	data := binarySTL(quad, "x")
	m, err := ParseSTL(data[:len(data)-50])
	if err != nil || len(m.Indices) != 3 {
		t.Fatalf("truncated: err=%v m=%v", err, m)
	}
}

// ---------- 3MF ----------

func make3MF(t *testing.T, files map[string]string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "t.3mf")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

const rels = `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Target="/3D/3dmodel.model" Id="rel0" Type="http://schemas.microsoft.com/3dmanufacturing/2013/01/3dmodel"/></Relationships>`

const tetra = `<mesh><vertices>
<vertex x="0" y="0" z="0"/><vertex x="10" y="0" z="0"/><vertex x="0" y="10" z="0"/><vertex x="0" y="0" z="10"/>
</vertices><triangles>
<triangle v1="0" v2="2" v3="1"/><triangle v1="0" v2="1" v3="3"/><triangle v1="1" v2="2" v3="3"/><triangle v1="2" v2="0" v3="3"/>
</triangles></mesh>`

func TestParse3MFMultipleBuildItems(t *testing.T) {
	model := `<?xml version="1.0"?><model unit="millimeter" xmlns="http://schemas.microsoft.com/3dmanufacturing/core/2015/02">
<resources>
<object id="1" name="Cube A" type="model">` + tetra + `</object>
<object id="2" type="model">` + tetra + `</object>
</resources>
<build>
<item objectid="1"/>
<item objectid="2" transform="1 0 0 0 1 0 0 0 1 100 200 300"/>
</build></model>`
	p := make3MF(t, map[string]string{"_rels/.rels": rels, "3D/3dmodel.model": model})
	m, err := Parse3MF(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Items) != 2 {
		t.Fatalf("items = %d", len(m.Items))
	}
	if m.Items[0].Name != "Cube A" || m.Items[1].Name != "Object 2" {
		t.Fatalf("names = %q, %q", m.Items[0].Name, m.Items[1].Name)
	}
	mn, mx := m.Items[1].Mesh.Bounds()
	if mn != [3]float32{100, 200, 300} || mx != [3]float32{110, 210, 310} {
		t.Fatalf("transform not applied: %v %v", mn, mx)
	}
	if len(m.Items[0].Mesh.Indices) != 12 {
		t.Fatalf("indices = %d", len(m.Items[0].Mesh.Indices))
	}
}

func TestParse3MFComponentsAndUnit(t *testing.T) {
	// object 3 は object 1 を 2 回 (平行移動違い) 参照。単位は cm → mm に換算される。
	model := `<?xml version="1.0"?><model unit="centimeter" xmlns="http://schemas.microsoft.com/3dmanufacturing/core/2015/02">
<resources>
<object id="1">` + tetra + `</object>
<object id="3"><components>
<component objectid="1"/>
<component objectid="1" transform="1 0 0 0 1 0 0 0 1 5 0 0"/>
</components></object>
</resources>
<build><item objectid="3" transform="1 0 0 0 1 0 0 0 1 0 0 1"/></build></model>`
	p := make3MF(t, map[string]string{"3D/3dmodel.model": model}) // .rels 無し → 既定パス
	m, err := Parse3MF(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Items) != 1 {
		t.Fatalf("items = %d", len(m.Items))
	}
	it := m.Items[0]
	if len(it.Mesh.Indices) != 24 {
		t.Fatalf("indices = %d (2 コンポーネント分)", len(it.Mesh.Indices))
	}
	mn, mx := it.Mesh.Bounds()
	// x: 0..10 と 5..15 → 0..15 cm = 0..150 mm / z: 1..11 cm → 10..110 mm
	if mn != [3]float32{0, 0, 10} || mx != [3]float32{150, 100, 110} {
		t.Fatalf("bounds = %v %v", mn, mx)
	}
}

func TestParse3MFTransformOrder(t *testing.T) {
	// コンポーネントの変換 → アイテムの変換 の順に適用される (行ベクトル規約)。
	// 子: +10 平行移動、アイテム: x を 2 倍。 (0 + 10) * 2 = 20
	model := `<model xmlns="http://schemas.microsoft.com/3dmanufacturing/core/2015/02"><resources>
<object id="1">` + tetra + `</object>
<object id="2"><components><component objectid="1" transform="1 0 0 0 1 0 0 0 1 10 0 0"/></components></object>
</resources><build><item objectid="2" transform="2 0 0 0 1 0 0 0 1 0 0 0"/></build></model>`
	m, err := Parse3MF(make3MF(t, map[string]string{"3D/3dmodel.model": model}))
	if err != nil {
		t.Fatal(err)
	}
	mn, mx := m.Items[0].Mesh.Bounds()
	if mn[0] != 20 || mx[0] != 40 {
		t.Fatalf("x range = %v..%v, want 20..40", mn[0], mx[0])
	}
}

func TestParse3MFNoBuildFallsBackToTopLevelObjects(t *testing.T) {
	model := `<model xmlns="http://schemas.microsoft.com/3dmanufacturing/core/2015/02"><resources>
<object id="1" name="part">` + tetra + `</object>
<object id="2" name="asm"><components><component objectid="1"/></components></object>
</resources></model>`
	m, err := Parse3MF(make3MF(t, map[string]string{"3D/3dmodel.model": model}))
	if err != nil {
		t.Fatal(err)
	}
	// 1 は 2 から参照されているので、トップレベルは 2 のみ。
	if len(m.Items) != 1 || m.Items[0].Name != "asm" {
		t.Fatalf("items = %+v", m.Items)
	}
}

func TestParse3MFExternalModelFile(t *testing.T) {
	// production 拡張: p:path で別ファイルのオブジェクトを参照 + Bambu 形式の名前メタデータ。
	main := `<model xmlns="http://schemas.microsoft.com/3dmanufacturing/core/2015/02" xmlns:p="http://schemas.microsoft.com/3dmanufacturing/production/2015/06"><resources>
<object id="2" type="model"><components><component p:path="/3D/Objects/o1.model" objectid="1"/></components></object>
</resources><build><item objectid="2"/></build></model>`
	sub := `<model xmlns="http://schemas.microsoft.com/3dmanufacturing/core/2015/02"><resources>
<object id="1" type="model">` + tetra + `</object></resources></model>`
	cfg := `<config><object id="2"><metadata key="name" value="Bracket"/><part id="1"><metadata key="name" value="inner"/></part></object></config>`
	m, err := Parse3MF(make3MF(t, map[string]string{
		"_rels/.rels":                    rels,
		"3D/3dmodel.model":               main,
		"3D/Objects/o1.model":            sub,
		"Metadata/model_settings.config": cfg,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Items) != 1 || m.Items[0].Name != "Bracket" || len(m.Items[0].Mesh.Indices) != 12 {
		t.Fatalf("items = %+v", m.Items)
	}
}

func TestParse3MFErrors(t *testing.T) {
	if _, err := Parse3MF(filepath.Join(t.TempDir(), "none.3mf")); err == nil {
		t.Error("missing file should fail")
	}
	bad := filepath.Join(t.TempDir(), "bad.3mf")
	if err := os.WriteFile(bad, []byte("not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse3MF(bad); err == nil {
		t.Error("non-zip should fail")
	}
	if _, err := Parse3MF(make3MF(t, map[string]string{"readme.txt": "x"})); err == nil {
		t.Error("archive without model should fail")
	}
	empty := `<model xmlns="http://schemas.microsoft.com/3dmanufacturing/core/2015/02"><resources/><build/></model>`
	if _, err := Parse3MF(make3MF(t, map[string]string{"3D/3dmodel.model": empty})); err == nil {
		t.Error("model without mesh should fail")
	}
	badIdx := `<model xmlns="x"><resources><object id="1"><mesh><vertices><vertex x="0" y="0" z="0"/></vertices>
<triangles><triangle v1="0" v2="1" v3="2"/></triangles></mesh></object></resources><build><item objectid="1"/></build></model>`
	if _, err := Parse3MF(make3MF(t, map[string]string{"3D/3dmodel.model": badIdx})); err == nil {
		t.Error("out-of-range triangle index should fail")
	}
	loop := `<model xmlns="x"><resources><object id="1"><components><component objectid="1"/></components></object></resources><build><item objectid="1"/></build></model>`
	if _, err := Parse3MF(make3MF(t, map[string]string{"3D/3dmodel.model": loop})); err == nil {
		t.Error("cyclic components should fail")
	}
}

func TestLoadDispatch(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "part.stl")
	if err := os.WriteFile(p, binarySTL(quad, "x"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if m.Format != "stl" || len(m.Items) != 1 || m.Items[0].Name != "part" {
		t.Fatalf("model = %+v", m)
	}
	if _, err := Load(filepath.Join(dir, "x.obj")); err == nil {
		t.Error("unsupported ext should fail")
	}
}

func TestParse3MFRejectsNonFinite(t *testing.T) {
	for _, bad := range []string{"NaN", "Inf", "-Inf", "1e999"} {
		model := `<model xmlns="x"><resources><object id="1"><mesh><vertices>
<vertex x="` + bad + `" y="0" z="0"/><vertex x="1" y="0" z="0"/><vertex x="0" y="1" z="0"/></vertices>
<triangles><triangle v1="0" v2="1" v3="2"/></triangles></mesh></object></resources><build><item objectid="1"/></build></model>`
		if _, err := Parse3MF(make3MF(t, map[string]string{"3D/3dmodel.model": model})); err == nil {
			t.Errorf("vertex %q should be rejected", bad)
		}
	}
	// 変換行列で float32 を溢れさせる場合もエラーにする (JSON 化できない値を出さない)。
	model := `<model xmlns="x"><resources><object id="1">` + tetra + `</object></resources>
<build><item objectid="1" transform="1e38 0 0 0 1 0 0 0 1 0 0 0"/><item objectid="1" transform="1e39 0 0 0 1 0 0 0 1 0 0 0"/></build></model>`
	if _, err := Parse3MF(make3MF(t, map[string]string{"3D/3dmodel.model": model})); err == nil {
		t.Error("overflowing transform should be rejected")
	}
}

func TestParse3MFLimits(t *testing.T) {
	oldT, oldV := maxTriangles, maxVertices
	defer func() { maxTriangles, maxVertices = oldT, oldV }()

	model := `<model xmlns="x"><resources><object id="1">` + tetra + `</object></resources><build><item objectid="1"/></build></model>`
	p := make3MF(t, map[string]string{"3D/3dmodel.model": model})

	maxTriangles, maxVertices = 3, 100 // 三角形 4 > 3
	if _, err := Parse3MF(p); err == nil {
		t.Error("triangle limit should apply while parsing")
	}
	maxTriangles, maxVertices = 100, 3 // 頂点 4 > 3
	if _, err := Parse3MF(p); err == nil {
		t.Error("vertex limit should apply while parsing")
	}
	maxTriangles, maxVertices = 100, 100
	if _, err := Parse3MF(p); err != nil {
		t.Errorf("within limits should pass: %v", err)
	}
}

func TestParse3MFExternalPathCaseInsensitive(t *testing.T) {
	// 同じ外部ファイルを大文字小文字・先頭 "/" 違いで参照しても解決できる。
	main := `<model xmlns="x" xmlns:p="y"><resources>
<object id="2"><components>
<component p:path="/3D/Objects/O1.model" objectid="1"/>
<component p:path="3d/objects/o1.model" objectid="1"/>
</components></object></resources><build><item objectid="2"/></build></model>`
	sub := `<model xmlns="x"><resources><object id="1">` + tetra + `</object></resources></model>`
	m, err := Parse3MF(make3MF(t, map[string]string{"3D/3dmodel.model": main, "3D/Objects/o1.model": sub}))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Items[0].Mesh.Indices) != 24 {
		t.Fatalf("indices = %d", len(m.Items[0].Mesh.Indices))
	}
	// 外部ファイル側の名前解決: p:path 付き build item は参照先のオブジェクト名を使う。
	main2 := `<model xmlns="x" xmlns:p="y"><resources></resources>
<build><item objectid="1" p:path="/3D/Objects/o1.model"/></build></model>`
	sub2 := `<model xmlns="x"><resources><object id="1" name="ExternalPart">` + tetra + `</object></resources></model>`
	m2, err := Parse3MF(make3MF(t, map[string]string{"3D/3dmodel.model": main2, "3D/Objects/o1.model": sub2}))
	if err != nil {
		t.Fatal(err)
	}
	if m2.Items[0].Name != "ExternalPart" {
		t.Fatalf("name = %q", m2.Items[0].Name)
	}
}

// ---------- LoadBytes ----------

func TestLoadBytesMatchesLoad(t *testing.T) {
	m3 := `<?xml version="1.0"?><model unit="centimeter" xmlns="http://schemas.microsoft.com/3dmanufacturing/core/2015/02">
<resources><object id="1" name="Part" type="model">` + tetra + `</object></resources>
<build><item objectid="1" transform="1 0 0 0 1 0 0 0 1 5 0 0"/></build></model>`
	p := make3MF(t, map[string]string{"_rels/.rels": rels, "3D/3dmodel.model": m3})
	want, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := LoadBytes("dir/Sample.3MF", data)
	if err != nil {
		t.Fatal(err)
	}
	if got.Format != "3mf" || len(got.Items) != 1 || got.Items[0].Name != "Part" ||
		!reflect.DeepEqual(got.Items[0].Mesh, want.Items[0].Mesh) {
		t.Fatalf("LoadBytes != Load: %+v vs %+v", got, want)
	}

	stl := binarySTL([][9]float32{{0, 0, 0, 1, 0, 0, 0, 1, 0}}, "hdr")
	ms, err := LoadBytes("a/b/tri.stl", stl)
	if err != nil || ms.Format != "stl" || ms.Items[0].Name != "tri" || len(ms.Items[0].Mesh.Indices) != 3 {
		t.Fatalf("stl: %+v %v", ms, err)
	}
}

func TestLoadBytesErrors(t *testing.T) {
	if _, err := LoadBytes("x.obj", []byte("x")); err == nil {
		t.Error("unsupported ext accepted")
	}
	if _, err := LoadBytes("x.3mf", []byte("not a zip")); err == nil {
		t.Error("broken 3mf accepted")
	}
	if _, err := LoadBytes("x.stl", []byte("solid x\nendsolid x\n")); err == nil {
		t.Error("empty stl accepted")
	}
}
