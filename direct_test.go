package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pageport/internal/direct"
	"pageport/internal/receiver"
)

func directRequest(a *app, method, path, body string) *httptest.ResponseRecorder {
	return request(a, method, path, strings.NewReader(body), true, map[string]string{"X-Pageport-Token": a.adminToken, "Content-Type": "application/json"})
}
func pairedTest(t *testing.T) (*app, string, string, string) {
	t.Helper()
	a := testApp(t)
	w := directRequest(a, "POST", "/api/devices", `{"name":"Test Aura"}`)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	d := a.direct.Devices[0]
	w = uploadFiles(a, map[string][]byte{"chapter.epub": []byte("exact bytes of an offered book")})
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	return a, d.ID, d.Token, a.books[0].ID
}
func queueTest(t *testing.T, a *app, device, file string) direct.Job {
	t.Helper()
	w := directRequest(a, "POST", "/api/transfers", `{"deviceId":"`+device+`","fileIds":["`+file+`"]}`)
	if w.Code != 201 {
		t.Fatalf("queue: %d %s", w.Code, w.Body.String())
	}
	var j direct.Job
	if json.Unmarshal(w.Body.Bytes(), &j) != nil {
		t.Fatal("invalid job")
	}
	return j
}

type testNativeUI struct {
	accept            bool
	confirms, imports int
	before            func()
}

func (u *testNativeUI) Confirm(_ context.Context, _ direct.Job) (bool, error) {
	u.confirms++
	if u.before != nil {
		u.before()
	}
	return u.accept, nil
}
func (u *testNativeUI) Import(context.Context) error { u.imports++; return nil }

func TestDirectAcceptBeforeBytesAndReceipt(t *testing.T) {
	a, device, token, file := pairedTest(t)
	job := queueTest(t, a, device, file)
	server := httptest.NewServer(a)
	defer server.Close()
	get := func(token, path string) *http.Response {
		r, _ := http.NewRequest("GET", server.URL+path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	path := "/receiver/jobs/" + job.ID + "/files/" + file
	resp := get(token, path)
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal("file accessible before acceptance")
	}
	resp = get(strings.Repeat("0", 64), "/receiver/poll")
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatal("unpaired receiver allowed")
	}
	ui := &testNativeUI{accept: true, before: func() {
		r := get(token, path)
		r.Body.Close()
		if r.StatusCode != 403 {
			t.Error("native confirmation not required")
		}
	}}
	out, receipts := t.TempDir(), t.TempDir()
	c, err := receiver.New(direct.Config{Server: server.URL, Token: token}, out, receipts, ui)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := c.Poll(context.Background())
	if err != nil || pending == nil || pending.Status != "awaiting_accept" {
		t.Fatal("no offer", err)
	}
	if err = c.Process(context.Background(), *pending); err != nil {
		t.Fatal(err)
	}
	if ui.confirms != 1 || ui.imports != 1 {
		t.Fatal("native workflow missing")
	}
	files, _ := os.ReadDir(out)
	if len(files) != 1 {
		t.Fatal("missing downloaded book")
	}
	data, _ := os.ReadFile(filepath.Join(out, files[0].Name()))
	if string(data) != "exact bytes of an offered book" {
		t.Fatal("changed file bytes")
	}
	if a.direct.Jobs[0].Status != "received" {
		t.Fatal("missing completion acknowledgement")
	}
	// Replaying after an interrupted acknowledgement must not prompt or save twice.
	if err = c.Process(context.Background(), *pending); err != nil {
		t.Fatal(err)
	}
	if ui.confirms != 1 || ui.imports != 1 {
		t.Fatal("replayed transfer")
	}
	w := directRequest(a, "GET", "/api/direct", "")
	if strings.Contains(w.Body.String(), token) {
		t.Fatal("pairing secret leaked in UI state")
	}
	resp = get(token, path)
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal("finished offer still downloadable")
	}
}

func TestDirectRejectCancelRevokeAndPause(t *testing.T) {
	a, device, token, file := pairedTest(t)
	job := queueTest(t, a, device, file)
	server := httptest.NewServer(a)
	defer server.Close()
	ui := &testNativeUI{accept: false}
	out := t.TempDir()
	c, _ := receiver.New(direct.Config{Server: server.URL, Token: token}, out, t.TempDir(), ui)
	pending, _ := c.Poll(context.Background())
	if err := c.Process(context.Background(), *pending); err != nil {
		t.Fatal(err)
	}
	files, _ := os.ReadDir(out)
	if len(files) != 0 || ui.imports != 0 || a.direct.Jobs[0].Status != "rejected" {
		t.Fatal("rejection downloaded books")
	}
	job = queueTest(t, a, device, file)
	w := directRequest(a, "DELETE", "/api/files/"+file, "")
	if w.Code != 409 {
		t.Fatal("deleted pending file")
	}
	w = directRequest(a, "DELETE", "/api/transfers/"+job.ID, "")
	if w.Code != 200 {
		t.Fatal("cancel failed")
	}
	if p, err := c.Poll(context.Background()); err != nil || p != nil {
		t.Fatal("cancelled offer returned")
	}
	directRequest(a, "POST", "/api/sharing", `{"enabled":false}`)
	if _, err := c.Poll(context.Background()); err == nil {
		t.Fatal("pause bypassed")
	}
	directRequest(a, "POST", "/api/sharing", `{"enabled":true}`)
	directRequest(a, "DELETE", "/api/devices/"+device, "")
	if _, err := c.Poll(context.Background()); err == nil {
		t.Fatal("revoked token accepted")
	}
}

func TestDirectChecksumFailureAndIsolation(t *testing.T) {
	a, device, token, file := pairedTest(t)
	job := queueTest(t, a, device, file)
	directRequest(a, "POST", "/api/devices", `{"name":"Other Kobo"}`)
	other := a.direct.Devices[1].Token
	server := httptest.NewServer(a)
	defer server.Close()
	c, _ := receiver.New(direct.Config{Server: server.URL, Token: other}, t.TempDir(), t.TempDir(), &testNativeUI{accept: true})
	if p, err := c.Poll(context.Background()); err != nil || p != nil {
		t.Fatal("another device received private offer")
	}
	r, _ := http.NewRequest("GET", server.URL+"/receiver/jobs/"+job.ID+"/files/"+file, nil)
	r.Header.Set("Authorization", "Bearer "+other)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatal("cross-device download")
	}
	// Alter the stored source after hashing the offer to exercise receiver verification.
	os.WriteFile(a.bookPath(file), []byte("tampered bytes"), 0600)
	ui := &testNativeUI{accept: true}
	out := t.TempDir()
	c, _ = receiver.New(direct.Config{Server: server.URL, Token: token}, out, t.TempDir(), ui)
	pending, _ := c.Poll(context.Background())
	if err = c.Process(context.Background(), *pending); err != nil {
		t.Fatal(err)
	}
	files, _ := os.ReadDir(out)
	if len(files) != 0 || ui.imports != 0 || a.direct.Jobs[0].Status != "failed" {
		t.Fatal("corrupt payload was published")
	}
}

func TestDirectRestartPreservesPairingAndFailsPending(t *testing.T) {
	a, device, token, file := pairedTest(t)
	queueTest(t, a, device, file)
	dir := a.dataDir
	a.Close()
	b, err := newApp(dir, testAssets)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if len(b.direct.Devices) != 1 || b.direct.Devices[0].Token != token || b.direct.Jobs[0].Status != "failed" {
		t.Fatal("incorrect restart state")
	}
	if !b.direct.Devices[0].LastSeen.IsZero() {
		t.Fatal("stale receiver shown online")
	}
}
