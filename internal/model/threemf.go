package model

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
)

// affine は 3MF の 4x3 変換行列 (行ベクトル規約: p' = p * M)。
// 並びは m00 m01 m02 m10 m11 m12 m20 m21 m22 m30 m31 m32。
type affine [12]float64

var identity = affine{1, 0, 0, 0, 1, 0, 0, 0, 1, 0, 0, 0}

// then は「a を適用した後に b を適用する」合成を返す。
func (a affine) then(b affine) affine {
	var r affine
	for row := 0; row < 3; row++ {
		for col := 0; col < 3; col++ {
			r[row*3+col] = a[row*3]*b[col] + a[row*3+1]*b[3+col] + a[row*3+2]*b[6+col]
		}
	}
	for col := 0; col < 3; col++ {
		r[9+col] = a[9]*b[col] + a[10]*b[3+col] + a[11]*b[6+col] + b[9+col]
	}
	return r
}

func (a affine) apply(x, y, z float64) (float64, float64, float64) {
	return x*a[0] + y*a[3] + z*a[6] + a[9],
		x*a[1] + y*a[4] + z*a[7] + a[10],
		x*a[2] + y*a[5] + z*a[8] + a[11]
}

func parseTransform(s string) (affine, error) {
	if strings.TrimSpace(s) == "" {
		return identity, nil
	}
	f := strings.Fields(s)
	if len(f) != 12 {
		return identity, fmt.Errorf("3MF: invalid transform %q", s)
	}
	var a affine
	for i, v := range f {
		x, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return identity, fmt.Errorf("3MF: invalid transform %q", s)
		}
		a[i] = x
	}
	return a, nil
}

type component struct {
	objectID  string
	file      string // 参照先モデルファイル (production 拡張の p:path)。"" なら同一ファイル
	transform affine
}

type object struct {
	id    string
	name  string
	verts []float32 // x,y,z の繰り返し
	tris  []uint32
	comps []component
}

type buildItem struct {
	objectID  string
	file      string
	transform affine
}

// modelFile は 1 つの .model (XML) の解析結果。
type modelFile struct {
	unitScale float64 // mm への換算係数
	objects   map[string]*object
	order     []string // objects の出現順
	build     []buildItem
	triCount  int // 読み込んだ三角形数
	vertCount int // 読み込んだ頂点数
}

// Parse3MF は 3MF (zip) を読み、build item ごとにメッシュを平坦化した Model を返す。
func Parse3MF(filename string) (*Model, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("3MF: %w", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("3MF: %w", err)
	}
	return Parse3MFReader(f, st.Size())
}

// Parse3MFReader は Parse3MF と同じ処理を、メモリ上などの io.ReaderAt (全長 size) に対して行う。
func Parse3MFReader(r io.ReaderAt, size int64) (*Model, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, fmt.Errorf("3MF: %w", err)
	}

	z := &zipFiles{files: map[string]*zip.File{}}
	for _, f := range zr.File {
		z.files[strings.ToLower(f.Name)] = f
	}
	rootPath := z.mainModelPath()
	root, err := z.loadModel(rootPath)
	if err != nil {
		return nil, err
	}

	names := z.objectNames() // Bambu / Orca の model_settings.config
	// z.open は大文字小文字・先頭 "/" を区別しないので、キャッシュキーも同じ規則で正規化する。
	normKey := func(n string) string { return strings.ToLower(strings.TrimPrefix(n, "/")) }
	loaded := map[string]*modelFile{normKey(rootPath): root}
	loadedTris := root.triCount   // 外部ファイルを含む総三角形数
	loadedVerts := root.vertCount // 外部ファイルを含む総頂点数
	getFile := func(name string) (*modelFile, error) {
		if name == "" {
			return root, nil
		}
		key := normKey(name)
		if mf, ok := loaded[key]; ok {
			return mf, nil
		}
		mf, err := z.loadModel(name)
		if err != nil {
			return nil, err
		}
		loadedTris += mf.triCount
		if loadedTris > maxTriangles {
			return nil, errors.New("3MF: too many triangles")
		}
		loadedVerts += mf.vertCount
		if loadedVerts > maxVertices {
			return nil, errors.New("3MF: too many vertices")
		}
		loaded[key] = mf
		return mf, nil
	}

	items := root.build
	if len(items) == 0 {
		// build が無い場合は、他から参照されていないオブジェクトを全て並べる。
		ref := map[string]bool{}
		for _, o := range root.objects {
			for _, c := range o.comps {
				if c.file == "" {
					ref[c.objectID] = true
				}
			}
		}
		for _, id := range root.order {
			if !ref[id] {
				items = append(items, buildItem{objectID: id, transform: identity})
			}
		}
	}

	m := &Model{Format: "3mf"}
	for i, it := range items {
		var mesh Mesh
		fl := &flattener{get: getFile, mesh: &mesh}
		mf, err := getFile(it.file)
		if err != nil {
			return nil, err
		}
		if err := fl.flatten(mf, it.objectID, it.transform, 0); err != nil {
			return nil, err
		}
		if len(mesh.Indices) == 0 {
			continue
		}
		// 単位を mm に統一する。
		if s := root.unitScale; s != 1 {
			for k := range mesh.Vertices {
				mesh.Vertices[k] = float32(float64(mesh.Vertices[k]) * s)
			}
		}
		// 参照先ファイル (p:path 付きの item なら外部ファイル) のオブジェクト名を優先し、
		// 無ければ model_settings.config の名前 (ルートのオブジェクト ID に対するもの) を使う。
		name := ""
		if o := mf.objects[it.objectID]; o != nil {
			name = o.name
		}
		if name == "" && it.file == "" {
			name = names[it.objectID]
		}
		if name == "" {
			name = "Object " + it.objectID
		}
		// 変換行列・単位の適用でオーバーフローした値も含め、非有限値は JSON 化できないので弾く。
		for _, v := range mesh.Vertices {
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
				return nil, fmt.Errorf("3MF: object %q has non-finite coordinates", it.objectID)
			}
		}
		m.Items = append(m.Items, Item{ID: strconv.Itoa(i), Name: name, Mesh: mesh})
	}
	if len(m.Items) == 0 {
		return nil, errors.New("3MF: no mesh found")
	}
	return m, nil
}

// maxDepth は components 入れ子の上限 (循環参照対策)。
const maxDepth = 32

type flattener struct {
	get   func(string) (*modelFile, error)
	mesh  *Mesh
	count int // 展開した三角形の総数 (爆発的な参照の抑止)
}

// 読み込みの上限。テストで差し替えられるよう var にしている。
var (
	maxTriangles = 20_000_000 // 1 つの 3MF 全体で許す三角形数
	maxVertices  = 20_000_000 // 1 つの 3MF 全体で許す頂点数
)

func (f *flattener) flatten(mf *modelFile, id string, t affine, depth int) error {
	if depth > maxDepth {
		return errors.New("3MF: components nested too deeply")
	}
	o := mf.objects[id]
	if o == nil {
		return fmt.Errorf("3MF: object %q not found", id)
	}
	if len(o.tris) > 0 {
		base := uint32(len(f.mesh.Vertices) / 3)
		for i := 0; i+2 < len(o.verts); i += 3 {
			x, y, z := t.apply(float64(o.verts[i]), float64(o.verts[i+1]), float64(o.verts[i+2]))
			f.mesh.Vertices = append(f.mesh.Vertices, float32(x), float32(y), float32(z))
		}
		for _, idx := range o.tris {
			f.mesh.Indices = append(f.mesh.Indices, base+idx)
		}
		f.count += len(o.tris) / 3
		if f.count > maxTriangles {
			return errors.New("3MF: too many triangles")
		}
	}
	for _, c := range o.comps {
		child := mf
		if c.file != "" {
			var err error
			if child, err = f.get(c.file); err != nil {
				return err
			}
		}
		if err := f.flatten(child, c.objectID, c.transform.then(t), depth+1); err != nil {
			return err
		}
	}
	return nil
}

type zipFiles struct {
	files map[string]*zip.File // 小文字化した名前 -> エントリ
}

func (z *zipFiles) open(name string) (io.ReadCloser, error) {
	f := z.files[strings.ToLower(strings.TrimPrefix(name, "/"))]
	if f == nil {
		return nil, fmt.Errorf("3MF: %s not found in archive", name)
	}
	if f.UncompressedSize64 > maxFileSize {
		return nil, fmt.Errorf("3MF: %s too large", name)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	// ヘッダ申告値を信用せず、実際に読む量も制限する (zip bomb 対策)。
	return struct {
		io.Reader
		io.Closer
	}{io.LimitReader(rc, maxFileSize), rc}, nil
}

// mainModelPath は _rels/.rels から 3D モデルのパスを求める。
func (z *zipFiles) mainModelPath() string {
	const fallback = "3D/3dmodel.model"
	rc, err := z.open("_rels/.rels")
	if err != nil {
		return fallback
	}
	defer rc.Close()
	dec := xml.NewDecoder(rc)
	for {
		tok, err := dec.Token()
		if err != nil {
			return fallback
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "Relationship" {
			continue
		}
		var typ, target string
		for _, a := range se.Attr {
			switch a.Name.Local {
			case "Type":
				typ = a.Value
			case "Target":
				target = a.Value
			}
		}
		if strings.HasSuffix(typ, "/3dmodel") && target != "" {
			return strings.TrimPrefix(target, "/")
		}
	}
}

// objectNames は Metadata/model_settings.config (Bambu Studio / OrcaSlicer) から
// オブジェクト名を拾う。無ければ空。
func (z *zipFiles) objectNames() map[string]string {
	names := map[string]string{}
	rc, err := z.open("Metadata/model_settings.config")
	if err != nil {
		return names
	}
	defer rc.Close()
	dec := xml.NewDecoder(rc)
	var cur string
	partDepth := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			return names
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "object":
				cur = attr(t, "id")
				partDepth = 0
			case "part":
				partDepth++
			case "metadata":
				if cur != "" && partDepth == 0 && attr(t, "key") == "name" {
					names[cur] = attr(t, "value")
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "object":
				cur = ""
			case "part":
				partDepth--
			}
		}
	}
}

func attr(se xml.StartElement, local string) string {
	for _, a := range se.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

var unitScales = map[string]float64{
	"micron":     0.001,
	"millimeter": 1,
	"centimeter": 10,
	"inch":       25.4,
	"foot":       304.8,
	"meter":      1000,
}

// loadModel は .model (XML) を解析する。
func (z *zipFiles) loadModel(name string) (*modelFile, error) {
	rc, err := z.open(name)
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	mf := &modelFile{unitScale: 1, objects: map[string]*object{}}
	dec := xml.NewDecoder(rc)
	var cur *object
	inBuild := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("3MF: %s: %w", name, err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "model":
				if u := attr(t, "unit"); u != "" {
					if s, ok := unitScales[strings.ToLower(u)]; ok {
						mf.unitScale = s
					}
				}
			case "object":
				cur = &object{id: attr(t, "id"), name: attr(t, "name")}
				mf.objects[cur.id] = cur
				mf.order = append(mf.order, cur.id)
			case "vertex":
				if cur == nil {
					continue
				}
				var p [3]float32
				for i, k := range []string{"x", "y", "z"} {
					v, err := strconv.ParseFloat(attr(t, k), 32)
					// ParseFloat は "NaN" / "Inf" も受理するので、有限値だけを通す。
					if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
						return nil, fmt.Errorf("3MF: invalid vertex in object %s", cur.id)
					}
					p[i] = float32(v)
				}
				mf.vertCount++
				if mf.vertCount > maxVertices {
					return nil, errors.New("3MF: too many vertices")
				}
				cur.verts = append(cur.verts, p[0], p[1], p[2])
			case "triangle":
				if cur == nil {
					continue
				}
				n := uint32(len(cur.verts) / 3)
				var idx [3]uint32
				for i, k := range []string{"v1", "v2", "v3"} {
					v, err := strconv.ParseUint(attr(t, k), 10, 32)
					if err != nil || uint32(v) >= n {
						return nil, fmt.Errorf("3MF: invalid triangle index in object %s", cur.id)
					}
					idx[i] = uint32(v)
				}
				mf.triCount++
				if mf.triCount > maxTriangles {
					return nil, errors.New("3MF: too many triangles")
				}
				cur.tris = append(cur.tris, idx[0], idx[1], idx[2])
			case "component":
				if cur == nil {
					continue
				}
				tr, err := parseTransform(attr(t, "transform"))
				if err != nil {
					return nil, err
				}
				cur.comps = append(cur.comps, component{
					objectID:  attr(t, "objectid"),
					file:      strings.TrimPrefix(attr(t, "path"), "/"),
					transform: tr,
				})
			case "build":
				inBuild = true
			case "item":
				if !inBuild {
					continue
				}
				tr, err := parseTransform(attr(t, "transform"))
				if err != nil {
					return nil, err
				}
				mf.build = append(mf.build, buildItem{
					objectID:  attr(t, "objectid"),
					file:      strings.TrimPrefix(attr(t, "path"), "/"),
					transform: tr,
				})
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "object":
				cur = nil
			case "build":
				inBuild = false
			}
		}
	}
	return mf, nil
}
