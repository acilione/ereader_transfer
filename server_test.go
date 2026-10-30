package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
)

var testAssets = fstest.MapFS{
	"index.html": {Data: []byte(`<html><meta name="pageport-token" content="__ADMIN_TOKEN__"></html>`)},
	"app.js":     {Data: []byte(`"use strict";`)},
}

func testApp(t *testing.T) *app {
	t.Helper()
	a, err := newApp(t.TempDir(), testAssets)
	if err != nil {
		t.Fatal(err)
	}
	a.port = 8080
	t.Cleanup(func() { _ = a.Close() })
	return a
}

func request(a *app, method, target string, body io.Reader, local bool, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, body)
	r.Host = "127.0.0.1:8080"
	r.RemoteAddr = "127.0.0.1:45678"
	if !local {
		r.Host = "192.168.1.8:8080"
		r.RemoteAddr = "192.168.1.9:45678"
	}
	for name, value := range headers {
		r.Header.Set(name, value)
	}
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	return w
}

func uploadFiles(a *app, files map[string][]byte) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	m := multipart.NewWriter(&buf)
	for name, data := range files {
		p, _ := m.CreateFormFile("files", name)
		_, _ = p.Write(data)
	}
	_ = m.Close()
	return request(a, "POST", "/api/files", &buf, true, map[string]string{"Content-Type": m.FormDataContentType(), "X-Pageport-Token": a.adminToken})
}

func addBook(t *testing.T, a *app, name string, data []byte) book {
	t.Helper()
	w := uploadFiles(a, map[string][]byte{name: data})
	if w.Code != 201 {
		t.Fatalf("upload returned %d: %s", w.Code, w.Body.String())
	}
	var result struct {
		Files []book `json:"files"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || len(result.Files) != 1 {
		t.Fatalf("invalid response: %s (%v)", w.Body.String(), err)
	}
	return result.Files[0]
}

func downloadPath(a *app, b book) string {
	return "/download/" + a.code + "/" + b.ID + "/" + url.PathEscape(b.Name)
}

func TestManagerAuthentication(t *testing.T) {
	a := testApp(t)
	for _, tc := range []struct {
		name, host, remote, origin, token string
		method                            string
		status                            int
	}{
		{"local read", "localhost:8080", "127.0.0.1:1", "", "", "GET", 200},
		{"IPv6 local read", "[::1]:8080", "[::1]:1", "", "", "GET", 200},
		{"LAN read blocked", "192.168.1.8:8080", "192.168.1.9:1", "", "", "GET", 403},
		{"LAN forged host blocked", "localhost:8080", "192.168.1.9:1", "", "", "GET", 403},
		{"DNS rebinding blocked", "attacker.test:8080", "127.0.0.1:1", "", "", "GET", 403},
		{"mutation needs token", "localhost:8080", "127.0.0.1:1", "", "", "POST", 403},
		{"wrong token blocked", "localhost:8080", "127.0.0.1:1", "", "wrong", "POST", 403},
		{"foreign origin blocked", "localhost:8080", "127.0.0.1:1", "http://attacker.test", a.adminToken, "POST", 403},
		{"null origin blocked", "localhost:8080", "127.0.0.1:1", "null", a.adminToken, "POST", 403},
		{"matching origin works", "localhost:8080", "127.0.0.1:1", "http://localhost:8080", a.adminToken, "POST", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := "/api/state"
			if tc.method == "POST" {
				target = "/api/sharing"
			}
			r := httptest.NewRequest(tc.method, target, strings.NewReader(`{"enabled":true}`))
			r.Host, r.RemoteAddr = tc.host, tc.remote
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("X-Pageport-Token", tc.token)
			w := httptest.NewRecorder()
			a.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("got %d: %s", w.Code, w.Body.String())
			}
		})
	}
	local := request(a, "GET", "/", nil, true, nil)
	if !strings.Contains(local.Body.String(), a.adminToken) {
		t.Fatal("desktop did not receive token")
	}
	remote := request(a, "GET", "/", nil, false, nil)
	if strings.Contains(remote.Body.String(), a.adminToken) || !strings.Contains(remote.Body.String(), "Six-digit") {
		t.Fatal("remote root must only receive pairing page")
	}
	for _, p := range []string{"/index.html", "/app.js", "/library.json", "/../library.json"} {
		if w := request(a, "GET", p, nil, false, nil); w.Code != 404 {
			t.Fatalf("remote static %s status %d", p, w.Code)
		}
	}
}

func TestFileTransferHeadersRangesAndPersistence(t *testing.T) {
	a := testApp(t)
	content := []byte("%PDF-1.7\nThis is the unchanged source file.\n")
	b := addBook(t, a, "L'été: a+b @home.PDF", content)
	if b.Name != "L'été: a+b @home.pdf" || b.Format != "PDF" {
		t.Fatalf("bad normalized metadata: %+v", b)
	}
	path := downloadPath(a, b)
	get := request(a, "GET", path, nil, false, nil)
	if get.Code != 200 || !bytes.Equal(get.Body.Bytes(), content) {
		t.Fatalf("file changed: %d %q", get.Code, get.Body.Bytes())
	}
	if get.Header().Get("Content-Type") != "application/pdf" || get.Header().Get("Content-Length") != fmt.Sprint(len(content)) {
		t.Fatal("wrong MIME/length headers", get.Header())
	}
	media, params, err := mime.ParseMediaType(get.Header().Get("Content-Disposition"))
	if err != nil || media != "attachment" || params["filename"] != b.Name {
		t.Fatalf("bad Content-Disposition: %s (%v)", get.Header().Get("Content-Disposition"), err)
	}
	if !strings.Contains(get.Header().Get("Content-Disposition"), "%3A") || !strings.Contains(get.Header().Get("Content-Disposition"), "%40") {
		t.Fatal("extended filename must encode reserved characters")
	}
	head := request(a, "HEAD", path, nil, false, nil)
	if head.Code != 200 || head.Body.Len() != 0 {
		t.Fatal("HEAD must have no body")
	}
	partial := request(a, "GET", path, nil, false, map[string]string{"Range": "bytes=0-3"})
	if partial.Code != 206 || partial.Body.String() != "%PDF" || partial.Header().Get("Content-Range") != fmt.Sprintf("bytes 0-3/%d", len(content)) {
		t.Fatalf("range failed: %d %q", partial.Code, partial.Body.String())
	}
	if a.books[0].Downloads != 1 || a.books[0].LastDownloadedAt == nil {
		t.Fatal("only full successful GET should count")
	}
	if w := request(a, "GET", strings.Replace(path, b.ID, "00000000000000000000000000000000", 1), nil, false, nil); w.Code != 404 {
		t.Fatal("unknown ID should fail")
	}
	if w := request(a, "GET", path+".epub", nil, false, nil); w.Code != 404 {
		t.Fatal("mismatched download filename should fail")
	}
	dir := a.dataDir
	_ = a.Close()
	reloaded, err := newApp(dir, testAssets)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	if len(reloaded.books) != 1 || reloaded.books[0].Downloads != 1 || reloaded.books[0].Name != b.Name {
		t.Fatal("library did not persist")
	}
	data, err := os.ReadFile(reloaded.bookPath(b.ID))
	if err != nil || !bytes.Equal(data, content) {
		t.Fatal("stored bytes did not persist")
	}
}

func TestKepubAndReaderHTML(t *testing.T) {
	a := testApp(t)
	b := addBook(t, a, `<b>Read & learn</b>.KEPUB`, []byte("PK test"))
	if b.Name != "b>.kepub.epub" { // Uploaded path components are stripped, including the slash in </b>.
		t.Fatalf("unexpected sanitized filename: %s", b.Name)
	}
	b = addBook(t, a, `<img onerror=evil>& Côte.EPUB`, []byte("PK second"))
	page := request(a, "GET", "/read/"+a.code, nil, false, nil)
	if page.Code != 200 || strings.Contains(page.Body.String(), "<script") || strings.Contains(page.Body.String(), "<img onerror") {
		t.Fatal("reader must escape names and require no scripts")
	}
	if !strings.Contains(page.Body.String(), "&lt;img onerror=evil&gt;&amp;") || !strings.Contains(page.Body.String(), "Download EPUB") {
		t.Fatal("reader did not render escaped title and download link")
	}
	if a.lastReaderSeen == nil {
		t.Fatal("remote reader activity not recorded")
	}
	a.lastReaderSeen = nil
	request(a, "GET", "/read/"+a.code, nil, true, nil)
	if a.lastReaderSeen != nil {
		t.Fatal("desktop preview must not count as reader seen")
	}
	filtered := request(a, "GET", "/read/"+a.code+"?q=nonexistent", nil, false, nil)
	if !strings.Contains(filtered.Body.String(), "No books match") {
		t.Fatal("search failed")
	}
	redirect := request(a, "GET", "/read?code="+a.code, nil, false, nil)
	if redirect.Code != 303 || redirect.Header().Get("Location") != "/read/"+a.code {
		t.Fatal("pairing redirect failed")
	}
	if w := request(a, "GET", "/read?code=../abc", nil, false, nil); w.Code != 400 {
		t.Fatal("invalid pairing input accepted")
	}
	_ = b
}

func TestUploadErrorsLeaveNoOrphanFiles(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content []byte
		limit   int64
		status  int
	}{
		{"empty.epub", nil, 32, 400},
		{"large.pdf", bytes.Repeat([]byte("a"), 33), 32, 413},
		{"license.acsm", []byte("license"), 32, 400},
		{"program.exe", []byte("data"), 32, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := testApp(t)
			a.uploadLimit = tc.limit
			w := uploadFiles(a, map[string][]byte{tc.name: tc.content})
			if w.Code != tc.status {
				t.Fatalf("got %d: %s", w.Code, w.Body.String())
			}
			if len(a.books) != 0 {
				t.Fatal("failed file entered library")
			}
			entries, _ := os.ReadDir(filepath.Join(a.dataDir, "books"))
			if len(entries) != 0 {
				t.Fatal("failed upload left an orphan")
			}
		})
	}
	a := testApp(t)
	var buf bytes.Buffer
	m := multipart.NewWriter(&buf)
	for _, name := range []string{"good.epub", "bad.exe"} {
		p, _ := m.CreateFormFile("files", name)
		_, _ = p.Write([]byte("data"))
	}
	_ = m.Close()
	w := request(a, "POST", "/api/files", &buf, true, map[string]string{"Content-Type": m.FormDataContentType(), "X-Pageport-Token": a.adminToken})
	if w.Code != 400 || len(a.books) != 0 {
		t.Fatal("multipart failure should roll back all files")
	}
	entries, _ := os.ReadDir(filepath.Join(a.dataDir, "books"))
	if len(entries) != 0 {
		t.Fatal("multipart failure left orphan")
	}
}

func TestPauseRotateAndDelete(t *testing.T) {
	a := testApp(t)
	b := addBook(t, a, "Book.epub", []byte("PK book"))
	headers := map[string]string{"X-Pageport-Token": a.adminToken}
	oldCode := a.code
	w := request(a, "POST", "/api/sharing", strings.NewReader(`{"enabled":false}`), true, headers)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if request(a, "GET", downloadPath(a, b), nil, false, nil).Code != 503 || request(a, "GET", "/read/"+a.code, nil, false, nil).Code != 503 {
		t.Fatal("pause did not gate reader and download")
	}
	request(a, "POST", "/api/sharing", strings.NewReader(`{"enabled":true}`), true, headers)
	if request(a, "POST", "/api/session/reset", nil, true, headers).Code != 200 || a.code == oldCode {
		t.Fatal("code did not rotate")
	}
	if request(a, "GET", "/read/"+oldCode, nil, false, nil).Code != 403 {
		t.Fatal("old pairing code still works")
	}
	if request(a, "GET", "/read/"+a.code, nil, false, nil).Code != 200 {
		t.Fatal("new pairing code should work")
	}
	if request(a, "DELETE", "/api/files/"+b.ID, nil, true, headers).Code != 200 {
		t.Fatal("delete failed")
	}
	if len(a.books) != 0 || request(a, "GET", downloadPath(a, b), nil, false, nil).Code != 404 {
		t.Fatal("deleted file remains available")
	}
	if _, err := os.Stat(a.bookPath(b.ID)); !os.IsNotExist(err) {
		t.Fatal("file not removed from storage")
	}
}

func TestPairingRateLimit(t *testing.T) {
	a := testApp(t)
	wrong := "000000"
	if a.code == wrong {
		wrong = "111111"
	}
	for i := 0; i < 12; i++ {
		if w := request(a, "GET", "/read/"+wrong, nil, false, nil); w.Code != 403 {
			t.Fatalf("attempt %d status %d", i, w.Code)
		}
	}
	w := request(a, "GET", "/read/"+a.code, nil, false, nil)
	if w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatal("incorrect codes were not rate limited")
	}
	request(a, "POST", "/api/session/reset", nil, true, map[string]string{"X-Pageport-Token": a.adminToken})
	if request(a, "GET", "/read/"+a.code, nil, false, nil).Code != 200 {
		t.Fatal("code reset must clear stale rate limit")
	}
}

func TestFilenameSanitization(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"../../secret.PDF", "secret.pdf"},
		{`C:\folder\Book.EPUB`, "Book.epub"},
		{"Book\r\n.PDF", "Book.pdf"},
		{"Book.KEPUB.EPUB", "Book.kepub.epub"},
	} {
		got, _, err := cleanFilename(tc.input)
		if err != nil || got != tc.want {
			t.Errorf("%q -> %q, %v; want %q", tc.input, got, err, tc.want)
		}
	}
	long, _, err := cleanFilename(strings.Repeat("é", 200) + ".EPUB")
	if err != nil || len(long) > 200 || !strings.HasSuffix(long, ".epub") {
		t.Fatal("long unicode filename incorrectly normalized")
	}
}

func TestLibraryLockAndInvalidMetadata(t *testing.T) {
	a := testApp(t)
	if second, err := newApp(a.dataDir, testAssets); err == nil {
		second.Close()
		t.Fatal("second process must not open same library")
	}
	_ = a.Close()
	second, err := newApp(a.dataDir, testAssets)
	if err != nil {
		t.Fatalf("lock not released: %v", err)
	}
	_ = second.Close()
	bad := `{"files":[{"id":"../../secret","name":"book.pdf"}]}`
	if err := os.WriteFile(filepath.Join(a.dataDir, "library.json"), []byte(bad), 0600); err != nil {
		t.Fatal(err)
	}
	if invalid, err := newApp(a.dataDir, testAssets); err == nil {
		invalid.Close()
		t.Fatal("unsafe metadata ID accepted")
	}
}

func TestConcurrentUploadsAndState(t *testing.T) {
	a := testApp(t)
	var wg sync.WaitGroup
	errs := make(chan string, 20)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w := uploadFiles(a, map[string][]byte{fmt.Sprintf("Book %d.epub", i): []byte("PK data")})
			if w.Code != 201 {
				errs <- w.Body.String()
			}
			if request(a, "GET", "/api/state", nil, true, nil).Code != 200 {
				errs <- "state failed"
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if len(a.books) != 10 {
		t.Fatalf("lost concurrent uploads: %d", len(a.books))
	}
}
