package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"pageport/internal/direct"
)

type pairedDevice struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Token    string    `json:"token"`
	LastSeen time.Time `json:"-"`
}
type directState struct {
	Devices []pairedDevice `json:"devices"`
	Jobs    []direct.Job   `json:"jobs"`
}

func (a *app) loadDirect() error {
	a.direct = directState{Devices: []pairedDevice{}, Jobs: []direct.Job{}}
	data, err := os.ReadFile(filepath.Join(a.dataDir, "direct.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = json.Unmarshal(data, &a.direct); err != nil {
		return err
	}
	for i := range a.direct.Jobs {
		j := &a.direct.Jobs[i]
		if direct.Active(j.Status) {
			j.Status = "failed"
			j.Message = "Pageport restarted. Send these books again."
		}
	}
	return a.saveDirect()
}

// Call with a.mu held. Tokens never appear in the public state response.
func (a *app) saveDirect() error {
	data, err := json.MarshalIndent(a.direct, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(a.dataDir, ".direct-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
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
	return os.Rename(f.Name(), filepath.Join(a.dataDir, "direct.json"))
}

func decodeDirect(w http.ResponseWriter, r *http.Request, value interface{}) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	d.DisallowUnknownFields()
	if d.Decode(value) != nil || d.Decode(new(interface{})) != io.EOF {
		jsonError(w, 400, "Invalid request.")
		return false
	}
	return true
}

func (a *app) directPublic() interface{} {
	devices := make([]map[string]interface{}, 0, len(a.direct.Devices))
	for _, d := range a.direct.Devices {
		devices = append(devices, map[string]interface{}{"id": d.ID, "name": d.Name, "online": !d.LastSeen.IsZero() && time.Since(d.LastSeen) < 25*time.Second, "lastSeen": d.LastSeen})
	}
	return map[string]interface{}{"devices": devices, "jobs": a.direct.Jobs, "receiverPackageAvailable": receiverArtifactPath("Pageport-kobo-receiver.zip") != ""}
}

// Receiver artifacts are optional companions to the portable desktop executable.
// Only fixed, locally managed filenames are resolved here.
func receiverArtifactPath(name string) string {
	executable, err := os.Executable()
	if err != nil {
		return ""
	}
	paths := []string{filepath.Join(filepath.Dir(executable), name), filepath.Join("dist", name)}
	if name == "Pageport-Kobo-setup.md" {
		paths = append(paths, filepath.Join("kobo", "README.md"))
	}
	for _, p := range paths {
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() {
			return p
		}
	}
	return ""
}

func serveReceiverArtifact(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "HEAD" {
		jsonError(w, 405, "Use GET to download the receiver package.")
		return
	}
	name := "Pageport-kobo-receiver.zip"
	if r.URL.Path == "/api/receiver-guide" {
		name = "Pageport-Kobo-setup.md"
	}
	path := receiverArtifactPath(name)
	if path == "" {
		jsonError(w, 404, "Extract the complete Pageport desktop ZIP into a folder. Keep its receiver ZIP and setup guide beside the executable.")
		return
	}
	f, err := os.Open(path)
	if err != nil {
		jsonError(w, 500, "Could not open receiver package.")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		jsonError(w, 500, "Could not read receiver package.")
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	if strings.HasSuffix(name, ".zip") {
		w.Header().Set("Content-Type", "application/zip")
	} else {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	http.ServeContent(w, r, name, info.ModTime(), f)
}

func (a *app) expireDirect() {
	changed := false
	for i := range a.direct.Jobs {
		j := &a.direct.Jobs[i]
		limit := 15 * time.Minute
		if j.Status == "receiving" {
			limit = time.Hour
		}
		if direct.Active(j.Status) && time.Since(j.UpdatedAt) > limit {
			j.Status, j.Message = "expired", "Transfer timed out. Wake the Kobo and send again."
			changed = true
		}
	}
	if changed {
		_ = a.saveDirect()
	}
}

func (a *app) serveDirectAdmin(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.expireDirect()
	switch {
	case r.URL.Path == "/api/direct" && r.Method == "GET":
		writeJSON(w, 200, a.directPublic())
	case r.URL.Path == "/api/devices" && r.Method == "POST":
		var input struct {
			Name string `json:"name"`
		}
		if !decodeDirect(w, r, &input) {
			return
		}
		input.Name = strings.TrimSpace(input.Name)
		if len(input.Name) < 1 || len(input.Name) > 60 || len(a.direct.Devices) >= 16 {
			jsonError(w, 400, "Choose a device name up to 60 bytes; at most 16 devices can be paired.")
			return
		}
		id, err := randomHex(16)
		if err != nil {
			jsonError(w, 500, "Could not pair device.")
			return
		}
		token, err := randomHex(32)
		if err != nil {
			jsonError(w, 500, "Could not pair device.")
			return
		}
		a.direct.Devices = append(a.direct.Devices, pairedDevice{ID: id, Name: input.Name, Token: token})
		if err = a.saveDirect(); err != nil {
			a.direct.Devices = a.direct.Devices[:len(a.direct.Devices)-1]
			jsonError(w, 500, "Could not save pairing.")
			return
		}
		writeJSON(w, 201, map[string]string{"id": id, "name": input.Name})
	case strings.HasPrefix(r.URL.Path, "/api/devices/"):
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/devices/"), "/")
		index := -1
		for i, d := range a.direct.Devices {
			if d.ID == parts[0] {
				index = i
				break
			}
		}
		if index < 0 {
			jsonError(w, 404, "Device not found.")
			return
		}
		if len(parts) == 2 && parts[1] == "config" && r.Method == "GET" {
			ip := r.URL.Query().Get("ip")
			valid := false
			for _, address := range a.addresses() {
				if address.IP == ip {
					valid = true
				}
			}
			if !valid {
				jsonError(w, 400, "Select this computer's Wi-Fi address first.")
				return
			}
			d := a.direct.Devices[index]
			w.Header().Set("Content-Disposition", `attachment; filename="config.json"`)
			writeJSON(w, 200, direct.Config{Server: "http://" + net.JoinHostPort(ip, strconv.Itoa(a.port)), Token: d.Token, DeviceName: d.Name})
		} else if len(parts) == 1 && r.Method == "DELETE" {
			old := append([]pairedDevice{}, a.direct.Devices...)
			a.direct.Devices = append(a.direct.Devices[:index], a.direct.Devices[index+1:]...)
			if err := a.saveDirect(); err != nil {
				a.direct.Devices = old
				jsonError(w, 500, "Could not remove device.")
				return
			}
			for i := range a.direct.Jobs {
				if a.direct.Jobs[i].DeviceID == parts[0] && direct.Active(a.direct.Jobs[i].Status) {
					a.direct.Jobs[i].Status = "cancelled"
					a.direct.Jobs[i].Message = "Device unpaired."
				}
			}
			_ = a.saveDirect()
			writeJSON(w, 200, map[string]bool{"removed": true})
		} else {
			jsonError(w, 404, "Endpoint not found.")
		}
	case r.URL.Path == "/api/transfers" && r.Method == "POST":
		var input struct {
			DeviceID string   `json:"deviceId"`
			FileIDs  []string `json:"fileIds"`
		}
		if !decodeDirect(w, r, &input) {
			return
		}
		if !a.sharing {
			jsonError(w, 409, "Resume sharing before sending books.")
			return
		}
		found := false
		for _, d := range a.direct.Devices {
			if d.ID == input.DeviceID {
				found = true
			}
		}
		if !found || len(input.FileIDs) == 0 || len(input.FileIDs) > 20 {
			jsonError(w, 400, "Choose a paired Kobo and 1–20 books.")
			return
		}
		for _, j := range a.direct.Jobs {
			if j.DeviceID == input.DeviceID && direct.Active(j.Status) {
				jsonError(w, 409, "This Kobo already has a pending transfer.")
				return
			}
		}
		files := []direct.File{}
		seen := map[string]bool{}
		for _, id := range input.FileIDs {
			if seen[id] {
				continue
			}
			seen[id] = true
			var selected *book
			for _, b := range a.books {
				if b.ID == id {
					copy := b
					selected = &copy
					break
				}
			}
			if selected == nil {
				jsonError(w, 404, "A selected book is no longer available.")
				return
			}
			f, err := os.Open(a.bookPath(id))
			if err != nil {
				jsonError(w, 500, "Could not read selected book.")
				return
			}
			h := sha256.New()
			n, err := io.Copy(h, f)
			f.Close()
			if err != nil || n != selected.Size {
				jsonError(w, 500, "Could not verify selected book.")
				return
			}
			files = append(files, direct.File{ID: id, Name: selected.Name, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))})
		}
		id, err := randomHex(16)
		if err != nil {
			jsonError(w, 500, "Could not create transfer.")
			return
		}
		now := time.Now().UTC()
		job := direct.Job{ID: id, DeviceID: input.DeviceID, Files: files, Status: "queued", Message: "Waiting for the Kobo receiver.", CreatedAt: now, UpdatedAt: now}
		if len(a.direct.Jobs) >= 100 {
			for i, old := range a.direct.Jobs {
				if !direct.Active(old.Status) {
					a.direct.Jobs = append(a.direct.Jobs[:i], a.direct.Jobs[i+1:]...)
					break
				}
			}
		}
		a.direct.Jobs = append(a.direct.Jobs, job)
		if err = a.saveDirect(); err != nil {
			a.direct.Jobs = a.direct.Jobs[:len(a.direct.Jobs)-1]
			jsonError(w, 500, "Could not queue transfer.")
			return
		}
		writeJSON(w, 201, job)
	case strings.HasPrefix(r.URL.Path, "/api/transfers/") && r.Method == "DELETE":
		id := strings.TrimPrefix(r.URL.Path, "/api/transfers/")
		for i := range a.direct.Jobs {
			j := &a.direct.Jobs[i]
			if j.ID != id {
				continue
			}
			if j.Status != "queued" && j.Status != "awaiting_accept" {
				jsonError(w, 409, "Only transfers awaiting acceptance can be cancelled.")
				return
			}
			old := *j
			j.Status = "cancelled"
			j.Message = "Cancelled on this computer."
			if err := a.saveDirect(); err != nil {
				*j = old
				jsonError(w, 500, "Could not cancel transfer.")
				return
			}
			writeJSON(w, 200, j)
			return
		}
		jsonError(w, 404, "Transfer not found.")
	default:
		jsonError(w, 404, "Endpoint not found.")
	}
}

func (a *app) serveReceiver(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	deviceID := ""
	for i := range a.direct.Devices {
		d := &a.direct.Devices[i]
		if len(token) == 64 && subtle.ConstantTimeCompare([]byte(token), []byte(d.Token)) == 1 {
			deviceID = d.ID
			d.LastSeen = time.Now().UTC()
			break
		}
	}
	if deviceID == "" {
		a.mu.Unlock()
		jsonError(w, 401, "Receiver pairing is invalid.")
		return
	}
	a.expireDirect()
	if r.URL.Path == "/receiver/heartbeat" && r.Method == "POST" {
		a.mu.Unlock()
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	if !a.sharing {
		a.mu.Unlock()
		jsonError(w, 503, "Sharing is paused.")
		return
	}
	if r.URL.Path == "/receiver/poll" && r.Method == "GET" {
		for i := range a.direct.Jobs {
			j := &a.direct.Jobs[i]
			if j.DeviceID == deviceID && direct.Active(j.Status) {
				if j.Status == "queued" {
					old := *j
					j.Status = "awaiting_accept"
					j.Message = "Tap Accept on your Kobo."
					j.UpdatedAt = time.Now().UTC()
					if err := a.saveDirect(); err != nil {
						*j = old
						a.mu.Unlock()
						jsonError(w, 500, "Could not update transfer.")
						return
					}
				}
				copy := *j
				a.mu.Unlock()
				writeJSON(w, 200, map[string]interface{}{"job": copy})
				return
			}
		}
		a.mu.Unlock()
		writeJSON(w, 200, map[string]interface{}{"job": nil})
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/receiver/jobs/"), "/")
	if len(parts) < 2 || !strings.HasPrefix(r.URL.Path, "/receiver/jobs/") {
		a.mu.Unlock()
		jsonError(w, 404, "Endpoint not found.")
		return
	}
	var j *direct.Job
	for i := range a.direct.Jobs {
		if a.direct.Jobs[i].ID == parts[0] && a.direct.Jobs[i].DeviceID == deviceID {
			j = &a.direct.Jobs[i]
			break
		}
	}
	if j == nil {
		a.mu.Unlock()
		jsonError(w, 404, "Transfer not found.")
		return
	}
	if parts[1] == "status" && len(parts) == 2 && r.Method == "POST" {
		var input struct {
			Status  string `json:"status"`
			Message string `json:"message"`
		}
		if !decodeDirect(w, r, &input) {
			a.mu.Unlock()
			return
		}
		allowed := (j.Status == "awaiting_accept" && (input.Status == "receiving" || input.Status == "rejected" || input.Status == "failed")) || (j.Status == "receiving" && (input.Status == "received" || input.Status == "saved" || input.Status == "failed"))
		if input.Status == j.Status {
			a.mu.Unlock()
			writeJSON(w, 200, map[string]bool{"ok": true})
			return
		}
		if !allowed || len(input.Message) > 500 {
			a.mu.Unlock()
			jsonError(w, 409, "Transfer is no longer available for this action.")
			return
		}
		old := *j
		j.Status = input.Status
		j.Message = input.Message
		j.UpdatedAt = time.Now().UTC()
		if err := a.saveDirect(); err != nil {
			*j = old
			a.mu.Unlock()
			jsonError(w, 500, "Could not save transfer status.")
			return
		}
		a.mu.Unlock()
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	if parts[1] == "files" && len(parts) == 3 && (r.Method == "GET" || r.Method == "HEAD") && j.Status == "receiving" {
		for _, b := range j.Files {
			if b.ID == parts[2] {
				f, err := os.Open(a.bookPath(b.ID))
				a.mu.Unlock()
				if err != nil {
					jsonError(w, 404, "Book unavailable.")
					return
				}
				defer f.Close()
				w.Header().Set("Content-Type", "application/octet-stream")
				http.ServeContent(w, r, b.Name, time.Time{}, f)
				return
			}
		}
	}
	a.mu.Unlock()
	jsonError(w, 403, "Accept this transfer on the Kobo before downloading its files.")
}
