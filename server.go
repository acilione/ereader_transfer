package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"math/big"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const maxFileBytes int64 = 512 << 20

var validID = regexp.MustCompile(`^[0-9a-f]{32}$`)
var supportedExtensions = []string{".epub", ".kepub.epub", ".kepub", ".pdf", ".cbz", ".cbr", ".txt", ".rtf", ".html", ".htm", ".mobi", ".jpg", ".jpeg", ".png", ".gif", ".bmp", ".tif", ".tiff"}

type book struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	Size             int64      `json:"size"`
	Format           string     `json:"format"`
	AddedAt          time.Time  `json:"addedAt"`
	Downloads        int        `json:"downloads"`
	LastDownloadedAt *time.Time `json:"lastDownloadedAt,omitempty"`
}

type address struct {
	IP  string `json:"ip"`
	URL string `json:"url"`
}

type failureWindow struct {
	Started time.Time
	Count   int
}

type app struct {
	mu             sync.Mutex
	dataDir        string
	assets         fs.FS
	port           int
	bind           string
	code           string
	adminToken     string
	sharing        bool
	books          []book
	lastReaderSeen *time.Time
	failures       map[string]failureWindow
	uploadLimit    int64
	lockFile       *os.File
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func randomCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

func newApp(dataDir string, assets fs.FS) (*app, error) {
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Join(abs, "books"), 0700); err != nil {
		return nil, err
	}
	lockFile, err := os.OpenFile(filepath.Join(abs, ".pageport.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = lockLibrary(lockFile); err != nil {
		lockFile.Close()
		return nil, errors.New("this library is already open in another Pageport instance; close it first or choose another --data directory")
	}
	keepLock := false
	defer func() {
		if !keepLock {
			lockFile.Close()
		}
	}()
	token, err := randomHex(32)
	if err != nil {
		return nil, err
	}
	code, err := randomCode()
	if err != nil {
		return nil, err
	}
	a := &app{dataDir: abs, assets: assets, code: code, adminToken: token, sharing: true, books: []book{}, failures: make(map[string]failureWindow), uploadLimit: maxFileBytes, bind: "0.0.0.0", lockFile: lockFile}
	data, err := os.ReadFile(filepath.Join(abs, "library.json"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		var stored struct {
			Files []book `json:"files"`
		}
		if err = json.Unmarshal(data, &stored); err != nil {
			return nil, fmt.Errorf("read library.json: %w", err)
		}
		seen := make(map[string]bool)
		for _, b := range stored.Files {
			clean, format, nameErr := cleanFilename(b.Name)
			if !validID.MatchString(b.ID) || seen[b.ID] || nameErr != nil || clean != b.Name {
				return nil, errors.New("library.json contains an invalid file record")
			}
			seen[b.ID] = true
			info, statErr := os.Stat(a.bookPath(b.ID))
			if errors.Is(statErr, os.ErrNotExist) {
				continue
			}
			if statErr != nil {
				return nil, statErr
			}
			if !info.Mode().IsRegular() {
				return nil, errors.New("library contains a non-regular file")
			}
			b.Size, b.Format = info.Size(), format
			a.books = append(a.books, b)
		}
	}
	if err = a.saveLocked(); err != nil {
		return nil, err
	}
	keepLock = true
	return a, nil
}

func (a *app) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.lockFile == nil {
		return nil
	}
	err := a.lockFile.Close()
	a.lockFile = nil
	return err
}

func (a *app) bookPath(id string) string { return filepath.Join(a.dataDir, "books", id) }

func (a *app) saveLocked() error {
	data, err := json.MarshalIndent(struct {
		Files []book `json:"files"`
	}{a.books}, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(a.dataDir, ".library-*.tmp")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(a.dataDir, "library.json"))
}

func (a *app) addresses() []address {
	interfaces, _ := net.Interfaces()
	type candidate struct {
		ip    string
		score int
	}
	var candidates []candidate
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		entries, _ := iface.Addrs()
		for _, entry := range entries {
			ip, _, err := net.ParseCIDR(entry.String())
			if err != nil || ip.To4() == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			if a.bind != "" && a.bind != "0.0.0.0" && a.bind != "::" && a.bind != ip.String() {
				continue
			}
			score := 2
			if ip.IsPrivate() {
				score = 0
			}
			name := strings.ToLower(iface.Name)
			if strings.Contains(name, "docker") || strings.Contains(name, "veth") || strings.Contains(name, "virtual") || strings.Contains(name, "wsl") || strings.Contains(name, "vethernet") || strings.Contains(name, "tun") || strings.Contains(name, "vpn") {
				score += 4
			}
			candidates = append(candidates, candidate{ip.String(), score})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score < candidates[j].score
		}
		return candidates[i].ip < candidates[j].ip
	})
	result := make([]address, 0, len(candidates))
	for _, item := range candidates {
		result = append(result, address{item.ip, "http://" + net.JoinHostPort(item.ip, strconv.Itoa(a.port)) + "/read/" + a.code})
	}
	return result
}

func remoteIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return net.ParseIP(host)
}

func loopbackHost(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func isAdmin(r *http.Request) bool {
	ip := remoteIP(r)
	return ip != nil && ip.IsLoopback() && loopbackHost(r.Host)
}

func (a *app) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	if strings.HasPrefix(r.URL.Path, "/api/") {
		if !isAdmin(r) {
			jsonError(w, http.StatusForbidden, "The library manager is only available on this computer.")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host {
			jsonError(w, http.StatusForbidden, "This request came from another website.")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Pageport-Token")), []byte(a.adminToken)) != 1 {
				jsonError(w, http.StatusForbidden, "Please reload Pageport and try again.")
				return
			}
		}
		a.serveAPI(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	switch {
	case r.URL.Path == "/":
		if isAdmin(r) {
			a.serveDesktop(w, r)
		} else {
			a.servePairing(w, "", http.StatusOK)
		}
	case r.URL.Path == "/read":
		code := r.URL.Query().Get("code")
		if len(code) != 6 || strings.Trim(code, "0123456789") != "" {
			a.servePairing(w, "Enter the six-digit code shown in Pageport.", http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "/read/"+code, http.StatusSeeOther)
	case strings.HasPrefix(r.URL.Path, "/read/"):
		a.serveReader(w, r)
	case strings.HasPrefix(r.URL.Path, "/download/"):
		a.serveDownload(w, r)
	default:
		if !isAdmin(r) {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if !fs.ValidPath(name) || name == "index.html" {
			http.NotFound(w, r)
			return
		}
		data, err := fs.ReadFile(a.assets, name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", mime.TypeByExtension(path.Ext(name)))
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		if r.Method != http.MethodHead {
			_, _ = w.Write(data)
		}
	}
}

func (a *app) serveDesktop(w http.ResponseWriter, r *http.Request) {
	data, err := fs.ReadFile(a.assets, "index.html")
	if err != nil {
		http.Error(w, "Pageport interface is missing", 500)
		return
	}
	data = []byte(strings.ReplaceAll(string(data), "__ADMIN_TOKEN__", a.adminToken))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}

func writeJSON(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func jsonError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func (a *app) serveAPI(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/api/state" && r.Method == http.MethodGet:
		a.mu.Lock()
		defer a.mu.Unlock()
		addresses := a.addresses()
		readerURL := "http://127.0.0.1:" + strconv.Itoa(a.port) + "/read/" + a.code
		if len(addresses) > 0 {
			readerURL = addresses[0].URL
		}
		total := 0
		for _, b := range a.books {
			total += b.Downloads
		}
		writeJSON(w, 200, map[string]interface{}{"appName": "Pageport", "version": version, "sessionCode": a.code, "sharing": a.sharing, "port": a.port, "addresses": addresses, "readerUrl": readerURL, "maxFileBytes": a.uploadLimit, "supportedExtensions": supportedExtensions, "files": a.books, "lastReaderSeen": a.lastReaderSeen, "totalDownloads": total})
	case r.URL.Path == "/api/files" && r.Method == http.MethodPost:
		a.upload(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/files/") && r.Method == http.MethodDelete:
		a.deleteBook(w, r)
	case r.URL.Path == "/api/sharing" && r.Method == http.MethodPost:
		var body struct {
			Enabled *bool `json:"enabled"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&body) != nil || body.Enabled == nil {
			jsonError(w, 400, "Expected an enabled setting.")
			return
		}
		a.mu.Lock()
		a.sharing = *body.Enabled
		a.mu.Unlock()
		writeJSON(w, 200, map[string]bool{"sharing": *body.Enabled})
	case r.URL.Path == "/api/session/reset" && r.Method == http.MethodPost:
		code, err := randomCode()
		if err != nil {
			jsonError(w, 500, "Could not generate a new pairing code.")
			return
		}
		a.mu.Lock()
		for code == a.code {
			code, err = randomCode()
			if err != nil {
				a.mu.Unlock()
				jsonError(w, 500, "Could not generate a new pairing code.")
				return
			}
		}
		a.code, a.lastReaderSeen = code, nil
		a.failures = make(map[string]failureWindow)
		a.mu.Unlock()
		writeJSON(w, 200, map[string]string{"sessionCode": code})
	default:
		jsonError(w, 404, "This endpoint or request method is not available.")
	}
}

func cleanFilename(raw string) (string, string, error) {
	raw = path.Base(strings.ReplaceAll(raw, "\\", "/"))
	raw = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u202e' || r == '\u202d' {
			return -1
		}
		return r
	}, raw))
	raw = strings.Trim(raw, ". ")
	if raw == "" || !utf8.ValidString(raw) {
		return "", "", errors.New("Choose a file with a valid name.")
	}
	lower := strings.ToLower(raw)
	ext := strings.ToLower(path.Ext(raw))
	if ext == ".acsm" {
		return "", "", errors.New("ACSM files are DRM licenses, not books. Use Adobe Digital Editions for these files.")
	}
	allowed := false
	for _, candidate := range supportedExtensions {
		if ext == candidate {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", "", errors.New("This file type is not supported. Choose an EPUB, PDF, comic, text, or image file.")
	}
	format := strings.ToUpper(strings.TrimPrefix(ext, "."))
	if ext == ".kepub" {
		raw += ".epub"
		format = "KEPUB"
		ext = ".kepub.epub"
	}
	if strings.HasSuffix(lower, ".kepub.epub") {
		format = "KEPUB"
		ext = ".kepub.epub"
	}
	raw = raw[:len(raw)-len(ext)] + ext
	if len(raw) > 200 {
		stem := strings.TrimSuffix(raw, raw[len(raw)-len(ext):])
		for len(stem)+len(ext) > 200 {
			_, size := utf8.DecodeLastRuneInString(stem)
			stem = stem[:len(stem)-size]
		}
		raw = stem + ext
	}
	return raw, format, nil
}

func (a *app) upload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, a.uploadLimit+(1<<20))
	reader, err := r.MultipartReader()
	if err != nil {
		jsonError(w, 400, "Choose a file to upload.")
		return
	}
	added := []book{}
	committed := false
	defer func() {
		if !committed {
			for _, b := range added {
				_ = os.Remove(a.bookPath(b.ID))
			}
		}
	}()
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			jsonError(w, 413, "Upload failed or exceeds the size limit. Try one file at a time.")
			return
		}
		if part.FormName() != "files" || part.FileName() == "" {
			part.Close()
			jsonError(w, 400, "Use the files upload field.")
			return
		}
		if len(added) >= 100 {
			part.Close()
			jsonError(w, 413, "Upload up to 100 files at a time.")
			return
		}
		name, format, err := cleanFilename(part.FileName())
		if err != nil {
			part.Close()
			jsonError(w, 400, err.Error())
			return
		}
		id, err := randomHex(16)
		if err != nil {
			part.Close()
			jsonError(w, 500, "Could not prepare the upload.")
			return
		}
		file, err := os.OpenFile(a.bookPath(id), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			part.Close()
			jsonError(w, 500, "Could not save this file. Check free space and the library directory.")
			return
		}
		b := book{ID: id, Name: name, Format: format, AddedAt: time.Now().UTC()}
		added = append(added, b)
		size, copyErr := io.Copy(file, io.LimitReader(part, a.uploadLimit+1))
		closeErr := file.Close()
		part.Close()
		if size > a.uploadLimit {
			jsonError(w, 413, "This file exceeds the 512 MB size limit.")
			return
		}
		if copyErr != nil {
			jsonError(w, 400, "The upload was interrupted. Please try again.")
			return
		}
		if closeErr != nil {
			jsonError(w, 500, "Could not finish saving the file.")
			return
		}
		if size == 0 {
			jsonError(w, 400, "This file is empty. Choose a file containing a book.")
			return
		}
		added[len(added)-1].Size = size
	}
	if len(added) == 0 {
		jsonError(w, 400, "Choose at least one file.")
		return
	}
	a.mu.Lock()
	original := a.books
	a.books = append(append([]book{}, a.books...), added...)
	if err := a.saveLocked(); err != nil {
		a.books = original
		a.mu.Unlock()
		jsonError(w, 500, "Could not save the library. Check free space and try again.")
		return
	}
	committed = true
	a.mu.Unlock()
	writeJSON(w, 201, map[string]interface{}{"files": added})
}

func (a *app) deleteBook(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/files/")
	if !validID.MatchString(id) {
		jsonError(w, 404, "This file is no longer in the library.")
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for i, b := range a.books {
		if b.ID != id {
			continue
		}
		original := a.books
		a.books = append(append([]book{}, a.books[:i]...), a.books[i+1:]...)
		if err := a.saveLocked(); err != nil {
			a.books = original
			jsonError(w, 500, "Could not update the library. Try again.")
			return
		}
		if err := os.Remove(a.bookPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
			a.books = original
			_ = a.saveLocked()
			jsonError(w, 409, "This file may still be downloading. Try removing it again in a moment.")
			return
		}
		writeJSON(w, 200, map[string]bool{"deleted": true})
		return
	}
	jsonError(w, 404, "This file is no longer in the library.")
}

func (a *app) authorizeReader(w http.ResponseWriter, r *http.Request, code string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	ip := remoteIP(r)
	key := ip.String()
	window := a.failures[key]
	now := time.Now()
	if now.Sub(window.Started) > time.Minute {
		window = failureWindow{Started: now}
	}
	if window.Count >= 12 {
		w.Header().Set("Retry-After", "60")
		a.servePairing(w, "Too many incorrect codes. Wait one minute and try again.", 429)
		return false
	}
	if subtle.ConstantTimeCompare([]byte(code), []byte(a.code)) != 1 {
		window.Count++
		if len(a.failures) >= 1024 {
			for k, v := range a.failures {
				if now.Sub(v.Started) > time.Minute {
					delete(a.failures, k)
				}
			}
		}
		if len(a.failures) < 1024 {
			a.failures[key] = window
		}
		a.servePairing(w, "This code is incorrect or has expired. Check the code in Pageport.", 403)
		return false
	}
	if !a.sharing {
		a.servePairing(w, "Sharing is paused. Resume sharing in Pageport on your computer.", 503)
		return false
	}
	return true
}

func (a *app) serveReader(w http.ResponseWriter, r *http.Request) {
	code := strings.TrimPrefix(r.URL.Path, "/read/")
	if !a.authorizeReader(w, r, code) {
		return
	}
	a.mu.Lock()
	if ip := remoteIP(r); ip != nil && !ip.IsLoopback() && r.Method != http.MethodHead {
		now := time.Now().UTC()
		a.lastReaderSeen = &now
	}
	books := append([]book{}, a.books...)
	a.mu.Unlock()
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) > 200 {
		q = q[:200]
	}
	items := make([]readerBook, 0, len(books))
	for _, b := range books {
		if q != "" && !strings.Contains(strings.ToLower(b.Name), strings.ToLower(q)) {
			continue
		}
		items = append(items, readerBook{b.Name, b.Format, humanSize(b.Size), "/download/" + code + "/" + b.ID + "/" + url.PathEscape(b.Name)})
	}
	sort.SliceStable(items, func(i, j int) bool { return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name) })
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.Method == http.MethodHead {
		return
	}
	_ = readerTemplate.Execute(w, struct {
		Code, Query string
		Files       []readerBook
	}{code, q, items})
}

type countingWriter struct {
	http.ResponseWriter
	written int64
	status  int
}

func (w *countingWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
		w.ResponseWriter.WriteHeader(status)
	}
}
func (w *countingWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	n, err := w.ResponseWriter.Write(p)
	w.written += int64(n)
	return n, err
}

func (a *app) serveDownload(w http.ResponseWriter, r *http.Request) {
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/download/"), "/", 3)
	if len(parts) != 3 || !validID.MatchString(parts[1]) {
		http.NotFound(w, r)
		return
	}
	if !a.authorizeReader(w, r, parts[0]) {
		return
	}
	a.mu.Lock()
	var selected *book
	for _, b := range a.books {
		if b.ID == parts[1] {
			copy := b
			selected = &copy
			break
		}
	}
	if selected == nil || selected.Name != parts[2] {
		a.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	file, err := os.Open(a.bookPath(selected.ID))
	a.mu.Unlock()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", bookMIME(selected.Format))
	ascii := strings.Map(func(r rune) rune {
		if r > 126 || r < 32 || r == '"' || r == '\\' {
			return '_'
		}
		return r
	}, selected.Name)
	w.Header().Set("Content-Disposition", "attachment; filename=\""+ascii+"\"; filename*=UTF-8''"+strings.ReplaceAll(url.QueryEscape(selected.Name), "+", "%20"))
	counter := &countingWriter{ResponseWriter: w}
	http.ServeContent(counter, r, selected.Name, selected.AddedAt, file)
	if r.Method == http.MethodGet && counter.status == 200 && counter.written == selected.Size && r.Context().Err() == nil {
		a.mu.Lock()
		for i := range a.books {
			if a.books[i].ID == selected.ID {
				now := time.Now().UTC()
				a.books[i].Downloads++
				a.books[i].LastDownloadedAt = &now
				_ = a.saveLocked()
				break
			}
		}
		a.mu.Unlock()
	}
}

func bookMIME(format string) string {
	switch format {
	case "EPUB", "KEPUB":
		return "application/epub+zip"
	case "PDF":
		return "application/pdf"
	case "CBZ":
		return "application/vnd.comicbook+zip"
	case "CBR":
		return "application/vnd.comicbook-rar"
	case "TXT":
		return "text/plain"
	case "RTF":
		return "application/rtf"
	case "HTML", "HTM":
		return "text/html"
	case "MOBI":
		return "application/x-mobipocket-ebook"
	case "JPG", "JPEG":
		return "image/jpeg"
	case "PNG":
		return "image/png"
	case "GIF":
		return "image/gif"
	case "BMP":
		return "image/bmp"
	case "TIF", "TIFF":
		return "image/tiff"
	default:
		return "application/octet-stream"
	}
}

func humanSize(n int64) string {
	if n >= 1<<20 {
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
	if n >= 1<<10 {
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}

type readerBook struct{ Name, Format, Size, URL string }

func (a *app) servePairing(w http.ResponseWriter, message string, status int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = pairingTemplate.Execute(w, message)
}

var pairingTemplate = template.Must(template.New("pairing").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Connect to Pageport</title>
<style>body{font-family:Arial,sans-serif;color:#000;background:#fff;margin:0;padding:30px;font-size:20px;line-height:1.5;max-width:700px}h1{font-size:34px}label{display:block;font-weight:bold;margin-top:24px}input{box-sizing:border-box;width:100%;padding:16px;font-size:30px;letter-spacing:8px;border:2px solid #000;margin:10px 0}button{display:block;width:100%;padding:18px;font-size:23px;background:#000;color:#fff;border:2px solid #000}small{display:block;margin-top:24px}p.error{border:2px solid #000;padding:16px}</style></head>
<body><h1>Pageport</h1><p>Your next read, ready to go.</p><p>Enter the pairing code shown in Pageport on your computer.</p>{{if .}}<p class="error">{{.}}</p>{{end}}<form action="/read" method="get"><label for="code">Six-digit pairing code</label><input id="code" name="code" type="text" inputmode="numeric" pattern="[0-9]{6}" maxlength="6" autocomplete="off" required><button type="submit">Open shared library</button></form><small>Keep your computer and Kobo on the same Wi-Fi, with Pageport running.</small></body></html>`))

var readerTemplate = template.Must(template.New("reader").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Your books · Pageport</title>
<style>body{font-family:Arial,sans-serif;color:#000;background:#fff;margin:0;padding:24px;font-size:20px;line-height:1.5;max-width:850px}h1{font-size:32px}a{color:#000}input{box-sizing:border-box;width:70%;padding:13px;font-size:20px;border:2px solid #000}button{padding:13px;font-size:20px;background:#fff;color:#000;border:2px solid #000}ul{list-style:none;padding:0}li{border-top:2px solid #000;padding:22px 0;word-wrap:break-word}h2{font-size:23px;margin:0 0 8px}.download{display:block;margin-top:14px;padding:16px;background:#000;color:#fff;text-decoration:none;text-align:center;border:2px solid #000;font-weight:bold}.note{font-size:17px}.refresh{display:inline-block;padding:10px 0}footer{border-top:2px solid #000;padding-top:20px;font-size:17px}</style></head>
<body><h1>Your shared library</h1><p>Tap a book to download it to this eReader.</p><form action="/read/{{.Code}}" method="get"><label for="q">Find a book</label><p><input id="q" name="q" value="{{.Query}}" type="search" maxlength="200"> <button type="submit">Find</button></p></form><a class="refresh" href="/read/{{.Code}}">Refresh all books</a>
<ul>{{range .Files}}<li><h2>{{.Name}}</h2><span>{{.Format}} · {{.Size}}</span><a class="download" href="{{.URL}}">Download {{.Format}}</a></li>{{else}}<li>{{if .Query}}No books match your search.{{else}}No books yet. Add files in Pageport on your computer, then refresh this page.{{end}}</li>{{end}}</ul>
<footer>Pageport · Shared over your local network.<p>After downloading, close the browser and check My Books. EPUB and PDF work best; other formats depend on your Kobo and its browser. DRM-protected books require their original authorization method.</p></footer></body></html>`))
