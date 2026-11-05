package server

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/m-tkg/3mf-viewer/internal/scan"
)

func stlBytes() []byte {
	b := make([]byte, 80)
	b = binary.LittleEndian.AppendUint32(b, 1)
	b = append(b, make([]byte, 12)...)
	for _, f := range []float32{0, 0, 0, 1, 0, 0, 0, 1, 0} {
		b = binary.LittleEndian.AppendUint32(b, math.Float32bits(f))
	}
	return append(b, 0, 0)
}

func newTestServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "root")
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "tri.stl"), stlBytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "secret.stl"), stlBytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "broken.stl"), []byte("solid x\nendsolid x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := scan.NewRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	s := New(Config{Root: r, Interval: 20 * time.Millisecond, Loopback: true, Version: "test"})
	ctx, cancel := context.WithCancel(context.Background())
	go s.Run(ctx)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { cancel(); ts.Close() })
	return ts, root
}

func get(t *testing.T, ts *httptest.Server, path string) (*http.Response, string) {
	t.Helper()
	res, err := http.Get(ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var sb strings.Builder
	sc := bufio.NewReader(res.Body)
	buf := make([]byte, 4096)
	for {
		n, err := sc.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return res, sb.String()
}

func TestFilesAndModel(t *testing.T) {
	ts, _ := newTestServer(t)

	res, body := get(t, ts, "/api/files")
	if res.StatusCode != 200 {
		t.Fatalf("status %d", res.StatusCode)
	}
	var files []scan.File
	if err := json.Unmarshal([]byte(body), &files); err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].Path != "broken.stl" || files[1].Path != "sub/tri.stl" {
		t.Fatalf("files = %+v", files)
	}

	res, body = get(t, ts, "/api/model?path=sub/tri.stl")
	if res.StatusCode != 200 {
		t.Fatalf("status %d: %s", res.StatusCode, body)
	}
	var m modelJSON
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatal(err)
	}
	if m.Format != "stl" || len(m.Items) != 1 || m.Items[0].TriangleCount != 1 || m.Items[0].VertexCount != 3 {
		t.Fatalf("model = %+v", m)
	}
	vb, err := base64.StdEncoding.DecodeString(m.Items[0].Vertices)
	if err != nil || len(vb) != 3*3*4 {
		t.Fatalf("vertices decode: %v len=%d", err, len(vb))
	}
	if m.Items[0].Max != [3]float32{1, 1, 0} {
		t.Fatalf("max = %v", m.Items[0].Max)
	}
}

func TestModelErrors(t *testing.T) {
	ts, _ := newTestServer(t)
	cases := []struct {
		path   string
		status int
	}{
		{"/api/model?path=../secret.stl", 403},
		{"/api/model?path=sub/../../secret.stl", 403},
		{"/api/model?path=%2e%2e%2fsecret.stl", 403},
		{"/api/model?path=", 403},
		{"/api/model", 403},
		{"/api/model?path=nope.stl", 404},
		{"/api/model?path=broken.stl", 422},
	}
	for _, c := range cases {
		res, body := get(t, ts, c.path)
		if res.StatusCode != c.status {
			t.Errorf("%s: status %d want %d (%s)", c.path, res.StatusCode, c.status, body)
		}
	}
}

func TestStaticAndHeaders(t *testing.T) {
	ts, _ := newTestServer(t)
	for _, p := range []string{"/", "/app.js", "/style.css", "/vendor/three.module.js", "/vendor/OrbitControls.js"} {
		res, body := get(t, ts, p)
		if res.StatusCode != 200 || len(body) == 0 {
			t.Errorf("%s: status %d len %d", p, res.StatusCode, len(body))
		}
		if res.Header.Get("X-Content-Type-Options") != "nosniff" || res.Header.Get("Content-Security-Policy") == "" {
			t.Errorf("%s: security headers missing", p)
		}
	}
	// 静的配信から、指定パスのファイルは見えない。
	if res, _ := get(t, ts, "/sub/tri.stl"); res.StatusCode != 404 {
		t.Errorf("model must not be served statically: %d", res.StatusCode)
	}
}

func TestMethodAndHostChecks(t *testing.T) {
	ts, _ := newTestServer(t)
	res, err := http.Post(ts.URL+"/api/files", "text/plain", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 405 {
		t.Errorf("POST status %d", res.StatusCode)
	}

	req, _ := http.NewRequest("GET", ts.URL+"/api/files", nil)
	req.Host = "evil.example.com" // DNS rebinding
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Errorf("foreign Host status %d", res.StatusCode)
	}
	for _, h := range []string{"localhost:3977", "127.0.0.1:1", "[::1]:80", "LOCALHOST"} {
		if !loopbackHost(h) {
			t.Errorf("loopbackHost(%q) = false", h)
		}
	}
	for _, h := range []string{"evil.com", "127.0.0.1.evil.com:80", "10.0.0.1:80", ""} {
		if loopbackHost(h) {
			t.Errorf("loopbackHost(%q) = true", h)
		}
	}
}

func TestEventsOnFileChanges(t *testing.T) {
	ts, root := newTestServer(t)
	res, err := http.Get(ts.URL + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type %q", ct)
	}
	lines := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	waitData := func() scan.Change {
		t.Helper()
		timeout := time.After(3 * time.Second)
		for {
			select {
			case l, ok := <-lines:
				if !ok {
					t.Fatal("stream closed")
				}
				if strings.HasPrefix(l, "data: ") {
					var c scan.Change
					if err := json.Unmarshal([]byte(strings.TrimPrefix(l, "data: ")), &c); err != nil {
						t.Fatal(err)
					}
					return c
				}
			case <-timeout:
				t.Fatal("timeout waiting for event")
			}
		}
	}

	// 追加
	if err := os.WriteFile(filepath.Join(root, "new.stl"), stlBytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if c := waitData(); len(c.Added) != 1 || c.Added[0] != "new.stl" {
		t.Fatalf("added: %+v", c)
	}
	// 変更 (サイズ変化)
	if err := os.WriteFile(filepath.Join(root, "new.stl"), append(stlBytes(), 0), 0o644); err != nil {
		t.Fatal(err)
	}
	if c := waitData(); len(c.Modified) != 1 || c.Modified[0] != "new.stl" {
		t.Fatalf("modified: %+v", c)
	}
	// 削除 (テストコード内の後始末ではなく、監視の検証対象としての削除)
	if err := os.Remove(filepath.Join(root, "new.stl")); err != nil {
		t.Fatal(err)
	}
	if c := waitData(); len(c.Removed) != 1 || c.Removed[0] != "new.stl" {
		t.Fatalf("removed: %+v", c)
	}
}

func TestModelCacheEviction(t *testing.T) {
	c := newModelCache(2, 100)
	c.put("a", make([]byte, 10))
	c.put("b", make([]byte, 10))
	c.put("c", make([]byte, 10))
	if _, ok := c.get("a"); ok {
		t.Error("a should be evicted by count")
	}
	c.put("big", make([]byte, 101))
	if _, ok := c.get("big"); ok {
		t.Error("oversize entry must not be cached")
	}
	c.put("d", make([]byte, 95))
	if _, ok := c.get("c"); ok {
		t.Error("c should be evicted by bytes")
	}
	if _, ok := c.get("d"); !ok {
		t.Error("d should be cached")
	}
}
