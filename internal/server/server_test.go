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
	"regexp"
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
	for _, p := range []string{"/", "/app.js", "/style.css", "/lib/three.module.js", "/lib/OrbitControls.js"} {
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

// ---------- 名前変更 API ----------

var tokenRe = regexp.MustCompile(`<meta name="viewer-token" content="([0-9a-f]+)">`)

func pageToken(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	_, body := get(t, ts, "/")
	m := tokenRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("token not embedded in index page")
	}
	return m[1]
}

type rnOpts struct {
	token, origin, site, ctype string
	host                       string
}

func postRename(t *testing.T, ts *httptest.Server, o rnOpts, path, name string) (int, string) {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"path": path, "name": name})
	req, _ := http.NewRequest("POST", ts.URL+"/api/rename", strings.NewReader(string(b)))
	if o.ctype == "" {
		o.ctype = "application/json"
	}
	req.Header.Set("Content-Type", o.ctype)
	if o.token != "" {
		req.Header.Set("X-Viewer-Token", o.token)
	}
	if o.origin != "" {
		req.Header.Set("Origin", o.origin)
	}
	if o.site != "" {
		req.Header.Set("Sec-Fetch-Site", o.site)
	}
	if o.host != "" {
		req.Host = o.host
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := res.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return res.StatusCode, sb.String()
}

func TestRenameAPI(t *testing.T) {
	ts, root := newTestServer(t)
	tok := pageToken(t, ts)
	ok := rnOpts{token: tok, origin: ts.URL, site: "same-origin"}

	// トークンは index.html にだけ埋め込まれ、他では露出しない。
	if _, b := get(t, ts, "/api/info"); strings.Contains(b, tok) {
		t.Error("token leaked in /api/info")
	}
	if _, b := get(t, ts, "/app.js"); strings.Contains(b, tok) {
		t.Error("token leaked in app.js")
	}

	// 正常系
	code, body := postRename(t, ts, ok, "sub/tri.stl", "renamed")
	if code != 200 || !strings.Contains(body, `"sub/renamed.stl"`) {
		t.Fatalf("rename: %d %s", code, body)
	}
	if _, err := os.Stat(filepath.Join(root, "sub", "renamed.stl")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "sub", "tri.stl")); !os.IsNotExist(err) {
		t.Fatal("old file remains")
	}
	// 新しい名前でモデル取得でき、古い名前は 404。
	if res, _ := get(t, ts, "/api/model?path=sub/renamed.stl"); res.StatusCode != 200 {
		t.Errorf("model after rename: %d", res.StatusCode)
	}
	if res, _ := get(t, ts, "/api/model?path=sub/tri.stl"); res.StatusCode != 404 {
		t.Errorf("old name model: %d", res.StatusCode)
	}

	// 上書き拒否 (409) で両方残る。
	if err := os.WriteFile(filepath.Join(root, "sub", "taken.stl"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _ = postRename(t, ts, ok, "sub/renamed.stl", "taken")
	if code != 409 {
		t.Errorf("overwrite: %d", code)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "sub", "taken.stl")); string(b) != "keep" {
		t.Fatal("destination overwritten")
	}
	if _, err := os.Stat(filepath.Join(root, "sub", "renamed.stl")); err != nil {
		t.Fatal(err)
	}

	// 配下外・不正名
	secret := filepath.Join(filepath.Dir(root), "secret.stl")
	for _, p := range []string{"../secret.stl", "sub/../../secret.stl", "/etc/passwd", ""} {
		if code, _ := postRename(t, ts, ok, p, "x"); code != 403 {
			t.Errorf("path %q: %d", p, code)
		}
	}
	if _, err := os.Stat(secret); err != nil {
		t.Fatal("outside file touched")
	}
	if code, _ := postRename(t, ts, ok, "nope.stl", "x"); code != 404 {
		t.Errorf("missing: %d", code)
	}
	for _, n := range []string{"", "a/b", "..", ".x", "a\x00", "../secret"} {
		if code, _ := postRename(t, ts, ok, "broken.stl", n); code != 400 {
			t.Errorf("name %q: %d", n, code)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "broken.stl")); err != nil {
		t.Fatal(err)
	}
}

func TestRenameAPIRejectsUnauthorized(t *testing.T) {
	ts, root := newTestServer(t)
	tok := pageToken(t, ts)
	cases := []struct {
		name string
		o    rnOpts
		want int
	}{
		{"no token", rnOpts{origin: ts.URL, site: "same-origin"}, 403},
		{"wrong token", rnOpts{token: strings.Repeat("0", len(tok)), origin: ts.URL, site: "same-origin"}, 403},
		{"no origin and no fetch-site", rnOpts{token: tok}, 403},
		{"foreign origin", rnOpts{token: tok, origin: "http://evil.example.com", site: "same-origin"}, 403},
		{"foreign origin only", rnOpts{token: tok, origin: "http://evil.example.com"}, 403},
		{"cross-site", rnOpts{token: tok, origin: ts.URL, site: "cross-site"}, 403},
		{"same-site", rnOpts{token: tok, site: "same-site"}, 403},
		{"null origin", rnOpts{token: tok, origin: "null"}, 403},
		{"foreign host", rnOpts{token: tok, origin: "http://evil.example.com", site: "same-origin", host: "evil.example.com"}, 403},
		{"form content-type", rnOpts{token: tok, origin: ts.URL, site: "same-origin", ctype: "application/x-www-form-urlencoded"}, 415},
	}
	for _, c := range cases {
		if code, _ := postRename(t, ts, c.o, "broken.stl", "hacked"); code != c.want {
			t.Errorf("%s: status %d want %d", c.name, code, c.want)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "broken.stl")); err != nil {
		t.Fatal("file was renamed by an unauthorized request")
	}

	// GET / PUT / DELETE は不可。他の API への POST も不可。
	if res, _ := get(t, ts, "/api/rename"); res.StatusCode != 405 {
		t.Errorf("GET rename: %d", res.StatusCode)
	}
	for _, m := range []string{"PUT", "DELETE", "PATCH"} {
		req, _ := http.NewRequest(m, ts.URL+"/api/rename", strings.NewReader("{}"))
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 405 {
			t.Errorf("%s rename: %d", m, res.StatusCode)
		}
	}
}

func TestRenameEventsAndCache(t *testing.T) {
	ts, _ := newTestServer(t)
	tok := pageToken(t, ts)
	// 改名前にモデルをキャッシュさせる。
	if res, _ := get(t, ts, "/api/model?path=sub/tri.stl"); res.StatusCode != 200 {
		t.Fatal(res.StatusCode)
	}
	res, err := http.Get(ts.URL + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	lines := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	if code, b := postRename(t, ts, rnOpts{token: tok, origin: ts.URL, site: "same-origin"}, "sub/tri.stl", "moved"); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	timeout := time.After(3 * time.Second)
	for {
		select {
		case l := <-lines:
			if !strings.HasPrefix(l, "data: ") {
				continue
			}
			var c scan.Change
			if err := json.Unmarshal([]byte(strings.TrimPrefix(l, "data: ")), &c); err != nil {
				t.Fatal(err)
			}
			if len(c.Added) != 1 || c.Added[0] != "sub/moved.stl" || len(c.Removed) != 1 || c.Removed[0] != "sub/tri.stl" {
				t.Fatalf("change: %+v", c)
			}
			return
		case <-timeout:
			t.Fatal("no SSE event after rename")
		}
	}
}

func TestModelCacheDropPath(t *testing.T) {
	c := newModelCache(10, 1000)
	c.put("/a/x.stl|1|2", make([]byte, 10))
	c.put("/a/x.stl.bak|1|2", make([]byte, 10))
	c.dropPath("/a/x.stl")
	if _, ok := c.get("/a/x.stl|1|2"); ok {
		t.Error("not dropped")
	}
	if _, ok := c.get("/a/x.stl.bak|1|2"); !ok {
		t.Error("unrelated entry dropped")
	}
	if c.bytes != 10 || len(c.keys) != 1 {
		t.Errorf("accounting: bytes=%d keys=%v", c.bytes, c.keys)
	}
}
