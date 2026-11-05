package model

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// welder は同一座標の頂点を 1 つにまとめながらメッシュを組み立てる。
type welder struct {
	mesh Mesh
	idx  map[[3]float32]uint32
}

func newWelder() *welder { return &welder{idx: map[[3]float32]uint32{}} }

func (w *welder) vertex(x, y, z float32) uint32 {
	k := [3]float32{x, y, z}
	if i, ok := w.idx[k]; ok {
		return i
	}
	i := uint32(len(w.mesh.Vertices) / 3)
	w.mesh.Vertices = append(w.mesh.Vertices, x, y, z)
	w.idx[k] = i
	return i
}

func (w *welder) triangle(a, b, c [3]float32) {
	w.mesh.Indices = append(w.mesh.Indices,
		w.vertex(a[0], a[1], a[2]),
		w.vertex(b[0], b[1], b[2]),
		w.vertex(c[0], c[1], c[2]))
}

// ParseSTL はバイナリ / ASCII 両対応で STL を読む。
func ParseSTL(data []byte) (*Mesh, error) {
	if isBinarySTL(data) {
		return parseBinarySTL(data)
	}
	return parseASCIISTL(data)
}

// isBinarySTL はヘッダの三角形数とファイルサイズの整合でバイナリ形式を判定する。
// ("solid" で始まるバイナリ STL があるため、先頭文字列だけでは判定しない)
func isBinarySTL(data []byte) bool {
	if len(data) < 84 {
		return false
	}
	n := binary.LittleEndian.Uint32(data[80:84])
	if uint64(len(data)) == 84+uint64(n)*50 {
		return true
	}
	// ASCII でなさそうなら、サイズが多少ずれていてもバイナリ扱いにする。
	head := bytes.TrimLeft(data[:5], " \t\r\n")
	return !bytes.HasPrefix(bytes.ToLower(head), []byte("solid"))
}

func parseBinarySTL(data []byte) (*Mesh, error) {
	if len(data) < 84 {
		return nil, errors.New("STL: file too short")
	}
	n := uint64(binary.LittleEndian.Uint32(data[80:84]))
	body := data[84:]
	if avail := uint64(len(body)) / 50; avail < n {
		n = avail // 切り詰められたファイルは読めた分だけ表示する
	}
	if n == 0 {
		return nil, errors.New("STL: no triangles")
	}
	w := newWelder()
	w.mesh.Indices = make([]uint32, 0, n*3)
	f := func(b []byte) float32 { return math.Float32frombits(binary.LittleEndian.Uint32(b)) }
	for i := uint64(0); i < n; i++ {
		rec := body[i*50 : i*50+50]
		var v [3][3]float32
		for k := 0; k < 3; k++ {
			o := 12 + k*12
			v[k] = [3]float32{f(rec[o:]), f(rec[o+4:]), f(rec[o+8:])}
		}
		if !finite(v) {
			continue
		}
		w.triangle(v[0], v[1], v[2])
	}
	if len(w.mesh.Indices) == 0 {
		return nil, errors.New("STL: no valid triangles")
	}
	return &w.mesh, nil
}

func parseASCIISTL(data []byte) (*Mesh, error) {
	w := newWelder()
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	var tri [][3]float32
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 4 && strings.EqualFold(fields[0], "vertex") {
			var p [3]float32
			for k := 0; k < 3; k++ {
				f, err := strconv.ParseFloat(fields[k+1], 32)
				if err != nil {
					return nil, fmt.Errorf("STL: invalid vertex %q", sc.Text())
				}
				p[k] = float32(f)
			}
			tri = append(tri, p)
			if len(tri) == 3 {
				if finite([3][3]float32{tri[0], tri[1], tri[2]}) {
					w.triangle(tri[0], tri[1], tri[2])
				}
				tri = tri[:0]
			}
		} else if len(fields) > 0 && strings.EqualFold(fields[0], "endloop") {
			tri = tri[:0]
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(w.mesh.Indices) == 0 {
		return nil, errors.New("STL: no triangles")
	}
	return &w.mesh, nil
}

func finite(v [3][3]float32) bool {
	for _, p := range v {
		for _, c := range p {
			if math.IsNaN(float64(c)) || math.IsInf(float64(c), 0) {
				return false
			}
		}
	}
	return true
}
