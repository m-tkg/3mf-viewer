// Package model は 3MF / STL を読み込み、プレビュー用の三角形メッシュに変換する。
package model

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Mesh は三角形メッシュ。Vertices は x,y,z の繰り返し、Indices は 3 要素ずつで 1 三角形。
type Mesh struct {
	Vertices []float32
	Indices  []uint32
}

// Bounds は AABB (min, max) を返す。頂点が無ければ零値。
func (m *Mesh) Bounds() (min, max [3]float32) {
	if len(m.Vertices) < 3 {
		return
	}
	copy(min[:], m.Vertices[:3])
	copy(max[:], m.Vertices[:3])
	for i := 3; i+2 < len(m.Vertices); i += 3 {
		for k := 0; k < 3; k++ {
			v := m.Vertices[i+k]
			if v < min[k] {
				min[k] = v
			}
			if v > max[k] {
				max[k] = v
			}
		}
	}
	return
}

// Item はプレビューで個別に選べる単位 (3MF の build item / STL 全体)。
type Item struct {
	ID   string
	Name string
	Mesh Mesh
}

// Model は読み込んだファイル。
type Model struct {
	Format string // "3mf" | "stl"
	Items  []Item
}

// maxFileSize は読み込みを許す最大ファイルサイズ (展開後の上限にも使う)。
const maxFileSize = 512 << 20

// Load は拡張子に応じて path を読み込む。
func Load(path string) (*Model, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".stl":
		data, err := readLimited(path)
		if err != nil {
			return nil, err
		}
		mesh, err := ParseSTL(data)
		if err != nil {
			return nil, err
		}
		name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		return &Model{Format: "stl", Items: []Item{{ID: "0", Name: name, Mesh: *mesh}}}, nil
	case ".3mf":
		return Parse3MF(path)
	}
	return nil, fmt.Errorf("unsupported file type: %s", filepath.Ext(path))
}

// LoadBytes は name (拡張子で種別を判定) の内容 data をメモリ上で読み込む。ディスクには触れない。
func LoadBytes(name string, data []byte) (*Model, error) {
	if int64(len(data)) > maxFileSize {
		return nil, fmt.Errorf("file too large: %d bytes", len(data))
	}
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".stl":
		mesh, err := ParseSTL(data)
		if err != nil {
			return nil, err
		}
		base := strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))
		return &Model{Format: "stl", Items: []Item{{ID: "0", Name: base, Mesh: *mesh}}}, nil
	case ".3mf":
		return Parse3MFReader(bytes.NewReader(data), int64(len(data)))
	}
	return nil, fmt.Errorf("unsupported file type: %s", ext)
}

// MaxFileSize は読み込みを許す最大ファイルサイズ (バイト)。
const MaxFileSize = maxFileSize

func readLimited(path string) ([]byte, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.Size() > maxFileSize {
		return nil, fmt.Errorf("file too large: %d bytes", st.Size())
	}
	return os.ReadFile(path)
}
