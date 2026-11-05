// Package server はファイル一覧・モデル配信・変更通知 (SSE)・静的ファイルを提供する HTTP サーバ。
package server

import (
	"context"
	"embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/m-tkg/3mf-viewer/internal/model"
	"github.com/m-tkg/3mf-viewer/internal/scan"
)

//go:embed all:static
var staticFS embed.FS

// Config はサーバ設定。
type Config struct {
	Root *scan.Root
	// Interval はファイル監視のポーリング間隔 (0 なら 1 秒)。
	Interval time.Duration
	// Loopback が true のとき、Host ヘッダをループバック名に限定する (DNS rebinding 対策)。
	Loopback bool
	Version  string
}

// Server は HTTP ハンドラ本体。
type Server struct {
	cfg   Config
	hub   *hub
	cache *modelCache
	mux   *http.ServeMux
}

// New は Server を作る。
func New(cfg Config) *Server {
	if cfg.Interval <= 0 {
		cfg.Interval = time.Second
	}
	s := &Server{cfg: cfg, hub: newHub(), cache: newModelCache(16, 256<<20)}
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	s.mux = http.NewServeMux()
	s.mux.HandleFunc("/api/info", s.handleInfo)
	s.mux.HandleFunc("/api/files", s.handleFiles)
	s.mux.HandleFunc("/api/model", s.handleModel)
	s.mux.HandleFunc("/api/events", s.handleEvents)
	files := http.FileServer(http.FS(sub))
	s.mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 埋め込みファイルは更新時刻を持たないため、バージョン更新後に古いキャッシュが残らないよう毎回検証させる。
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	}))
	return s
}

// Handler は共通ミドルウェア込みのハンドラを返す。
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Loopback && !loopbackHost(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data: blob:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'")
		s.mux.ServeHTTP(w, r)
	})
}

// loopbackHost は Host ヘッダ値がループバックを指すか。
func loopbackHost(host string) bool {
	h := host
	if hh, _, err := net.SplitHostPort(host); err == nil {
		h = hh
	}
	h = strings.Trim(h, "[]")
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// Run はファイル監視ループを ctx が終わるまで回す。
func (s *Server) Run(ctx context.Context) {
	prev, _ := s.cfg.Root.List()
	t := time.NewTicker(s.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			cur, err := s.cfg.Root.List()
			if err != nil {
				continue
			}
			if c := scan.Diff(prev, cur); !c.Empty() {
				s.hub.broadcast(c)
			}
			prev = cur
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"root": s.cfg.Root.Dir, "version": s.cfg.Version})
}

func (s *Server) handleFiles(w http.ResponseWriter, r *http.Request) {
	files, err := s.cfg.Root.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if files == nil {
		files = []scan.File{}
	}
	writeJSON(w, http.StatusOK, files)
}

// itemJSON は API 上のモデル 1 件。頂点・インデックスは little endian バイナリの base64。
type itemJSON struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	Vertices      string     `json:"vertices"`
	Indices       string     `json:"indices"`
	VertexCount   int        `json:"vertexCount"`
	TriangleCount int        `json:"triangleCount"`
	Min           [3]float32 `json:"min"`
	Max           [3]float32 `json:"max"`
}

type modelJSON struct {
	Format string     `json:"format"`
	Items  []itemJSON `json:"items"`
}

func encodeModel(m *model.Model) ([]byte, error) {
	out := modelJSON{Format: m.Format, Items: make([]itemJSON, 0, len(m.Items))}
	for _, it := range m.Items {
		vb := make([]byte, 4*len(it.Mesh.Vertices))
		for i, v := range it.Mesh.Vertices {
			binary.LittleEndian.PutUint32(vb[i*4:], math.Float32bits(v))
		}
		ib := make([]byte, 4*len(it.Mesh.Indices))
		for i, v := range it.Mesh.Indices {
			binary.LittleEndian.PutUint32(ib[i*4:], v)
		}
		mn, mx := it.Mesh.Bounds()
		out.Items = append(out.Items, itemJSON{
			ID:            it.ID,
			Name:          it.Name,
			Vertices:      base64.StdEncoding.EncodeToString(vb),
			Indices:       base64.StdEncoding.EncodeToString(ib),
			VertexCount:   len(it.Mesh.Vertices) / 3,
			TriangleCount: len(it.Mesh.Indices) / 3,
			Min:           mn,
			Max:           mx,
		})
	}
	return json.Marshal(out)
}

func (s *Server) handleModel(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	abs, err := s.cfg.Root.Resolve(rel)
	if err != nil {
		if errors.Is(err, scan.ErrNotAllowed) {
			writeErr(w, http.StatusForbidden, "path not allowed")
		} else if errors.Is(err, os.ErrNotExist) {
			writeErr(w, http.StatusNotFound, "file not found")
		} else {
			writeErr(w, http.StatusBadRequest, "invalid path")
		}
		return
	}
	st, err := os.Stat(abs)
	if err != nil || !st.Mode().IsRegular() {
		writeErr(w, http.StatusNotFound, "file not found")
		return
	}
	key := fmt.Sprintf("%s|%d|%d", abs, st.Size(), st.ModTime().UnixNano())
	body, ok := s.cache.get(key)
	if !ok {
		m, err := model.Load(abs)
		if err != nil {
			writeErr(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		if body, err = encodeModel(m); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.cache.put(key, body)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	ch, cancel := s.hub.subscribe()
	defer cancel()
	fmt.Fprint(w, ": connected\n\n")
	fl.Flush()
	ka := time.NewTicker(20 * time.Second)
	defer ka.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case c := <-ch:
			b, _ := json.Marshal(c)
			fmt.Fprintf(w, "event: change\ndata: %s\n\n", b)
			fl.Flush()
		case <-ka.C:
			fmt.Fprint(w, ": keepalive\n\n")
			fl.Flush()
		}
	}
}

// hub は SSE 購読者への変更通知を配る。
type hub struct {
	mu   sync.Mutex
	subs map[chan scan.Change]struct{}
}

func newHub() *hub { return &hub{subs: map[chan scan.Change]struct{}{}} }

func (h *hub) subscribe() (<-chan scan.Change, func()) {
	ch := make(chan scan.Change, 8)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}
}

func (h *hub) broadcast(c scan.Change) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- c:
		default: // 遅い購読者は取りこぼす (クライアントは一覧を再取得するだけ)
		}
	}
}

// modelCache はエンコード済みモデルの小さなキャッシュ (挿入順で追い出す)。
type modelCache struct {
	mu       sync.Mutex
	max      int
	maxBytes int
	bytes    int
	keys     []string
	m        map[string][]byte
}

func newModelCache(max, maxBytes int) *modelCache {
	return &modelCache{max: max, maxBytes: maxBytes, m: map[string][]byte{}}
}

func (c *modelCache) get(k string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.m[k]
	return b, ok
}

func (c *modelCache) put(k string, b []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(b) > c.maxBytes {
		return
	}
	if _, ok := c.m[k]; ok {
		return
	}
	for len(c.keys) > 0 && (len(c.keys) >= c.max || c.bytes+len(b) > c.maxBytes) {
		old := c.keys[0]
		c.keys = c.keys[1:]
		c.bytes -= len(c.m[old])
		delete(c.m, old)
	}
	c.m[k] = b
	c.keys = append(c.keys, k)
	c.bytes += len(b)
}
