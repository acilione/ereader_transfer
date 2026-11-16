// Package receiver downloads accepted offers using a paired device credential.
package receiver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"pageport/internal/direct"
)

var idPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
var hashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type UI interface {
	Confirm(context.Context, direct.Job) (bool, error)
	Import(context.Context) error
}
type Client struct {
	Config   direct.Config
	Output   string
	Receipts string
	HTTP     *http.Client
	UI       UI
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func New(config direct.Config, output, receipts string, ui UI) (*Client, error) {
	u, err := url.Parse(config.Server)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || !hashPattern.MatchString(config.Token) {
		return nil, errors.New("invalid receiver configuration; download config.json from Pageport again")
	}
	config.Server = strings.TrimRight(config.Server, "/")
	return &Client{Config: config, Output: output, Receipts: receipts, UI: ui, HTTP: &http.Client{Timeout: 30 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("receiver redirects are not permitted") }}}, nil
}

func (c *Client) request(ctx context.Context, method, path string, value interface{}) (*http.Response, error) {
	var body io.Reader
	if value != nil {
		data, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(data)
	}
	r, err := http.NewRequestWithContext(ctx, method, c.Config.Server+path, body)
	if err != nil {
		return nil, err
	}
	r.Header.Set("Authorization", "Bearer "+c.Config.Token)
	if value != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	response, err := c.HTTP.Do(r)
	if err != nil {
		return nil, errors.New("cannot reach Pageport on the configured Wi-Fi address")
	}
	if response.StatusCode/100 != 2 {
		response.Body.Close()
		return nil, fmt.Errorf("Pageport returned HTTP %d", response.StatusCode)
	}
	return response, nil
}

func (c *Client) Heartbeat(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	r, err := c.request(ctx, "POST", "/receiver/heartbeat", nil)
	if err == nil {
		r.Body.Close()
	}
	return err
}
func (c *Client) Poll(ctx context.Context) (*direct.Job, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	r, err := c.request(ctx, "GET", "/receiver/poll", nil)
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	var response struct {
		Job *direct.Job `json:"job"`
	}
	err = json.NewDecoder(io.LimitReader(r.Body, 65536)).Decode(&response)
	return response.Job, err
}
func (c *Client) status(ctx context.Context, j direct.Job, status, message string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	r, err := c.request(ctx, "POST", "/receiver/jobs/"+j.ID+"/status", map[string]string{"status": status, "message": message})
	if err == nil {
		r.Body.Close()
	}
	return err
}

func verifyFile(path string, f direct.File) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != f.Size {
		return false
	}
	r, err := os.Open(path)
	if err != nil {
		return false
	}
	defer r.Close()
	h := sha256.New()
	_, err = io.Copy(h, r)
	return err == nil && hex.EncodeToString(h.Sum(nil)) == f.SHA256
}

func validateJob(j direct.Job) error {
	if !idPattern.MatchString(j.ID) || len(j.Files) < 1 || len(j.Files) > 20 {
		return errors.New("invalid transfer manifest")
	}
	seen := map[string]bool{}
	for _, f := range j.Files {
		if !idPattern.MatchString(f.ID) || seen[f.ID] || !hashPattern.MatchString(f.SHA256) || f.Size <= 0 || f.Size > 512<<20 || len(f.Name) > 200 || f.Name == "" || filepath.Base(f.Name) != f.Name || strings.ContainsAny(f.Name, "/\\\x00\r\n") || strings.HasPrefix(f.Name, ".") {
			return errors.New("invalid book in transfer manifest")
		}
		seen[f.ID] = true
	}
	return nil
}

// Process never requests file contents until the native dialog returns Accept.
func (c *Client) Process(ctx context.Context, j direct.Job) error {
	if err := validateJob(j); err != nil {
		return err
	}
	if err := os.MkdirAll(c.Receipts, 0700); err != nil {
		return err
	}
	receiptPath := filepath.Join(c.Receipts, j.ID+".json")
	var receipt struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	if data, err := os.ReadFile(receiptPath); err == nil && json.Unmarshal(data, &receipt) == nil && (receipt.Status == "received" || receipt.Status == "saved" || receipt.Status == "rejected" || receipt.Status == "failed") {
		return c.status(ctx, j, receipt.Status, receipt.Message)
	}
	finish := func(status, message string) error {
		data, _ := json.Marshal(map[string]string{"status": status, "message": message})
		if err := os.WriteFile(receiptPath+".tmp", data, 0600); err != nil {
			return err
		}
		if err := os.Rename(receiptPath+".tmp", receiptPath); err != nil {
			return err
		}
		return c.status(ctx, j, status, message)
	}
	accepted, err := c.UI.Confirm(ctx, j)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return finish("failed", "The Kobo could not show or complete the confirmation dialog.")
	}
	if !accepted {
		return finish("rejected", "Declined on the Kobo.")
	}
	if err = c.status(ctx, j, "receiving", "Accepted on Kobo. Receiving books…"); err != nil {
		return err
	}
	if err = os.MkdirAll(c.Output, 0755); err != nil {
		return finish("failed", "Cannot create the Kobo books folder.")
	}
	for _, f := range j.Files {
		if err = c.download(ctx, j, f); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return finish("failed", "Download interrupted or verification failed. Some files may already be saved; send again to retry.")
		}
	}
	if err = c.UI.Import(ctx); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return finish("saved", "Books saved and verified. Library rescan did not finish; use Import books in NickelMenu.")
	}
	return finish("received", "Files verified and saved; Kobo library scan completed.")
}

func (c *Client) download(ctx context.Context, j direct.Job, f direct.File) error {
	ext := filepath.Ext(f.Name)
	stem := strings.TrimSuffix(f.Name, ext)
	if strings.HasSuffix(strings.ToLower(f.Name), ".kepub.epub") {
		ext = ".kepub.epub"
		stem = f.Name[:len(f.Name)-len(ext)]
	}
	destination := filepath.Join(c.Output, stem+"-"+f.ID[:8]+ext)
	if _, err := os.Lstat(destination); err == nil {
		if verifyFile(destination, f) {
			return nil
		}
		return errors.New("destination already exists with different contents")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	r, err := c.request(ctx, "GET", "/receiver/jobs/"+j.ID+"/files/"+f.ID, nil)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	// Stage on the same filesystem, under a hidden name Nickel will not import.
	tmp, err := os.CreateTemp(c.Output, ".pageport-*.part")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(r.Body, f.Size+1))
	if err != nil || n != f.Size || hex.EncodeToString(h.Sum(nil)) != f.SHA256 {
		tmp.Close()
		return errors.New("download does not match its size and SHA-256")
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// FAT has no hard links. Create exclusively, then copy the verified staging
	// file. Stop promptly on USB connection and remove incomplete output.
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	source, err := os.Open(tmp.Name())
	if err != nil {
		out.Close()
		os.Remove(destination)
		return err
	}
	_, copyErr := io.Copy(out, contextReader{ctx, source})
	source.Close()
	syncErr := out.Sync()
	closeErr := out.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil {
		os.Remove(destination)
		return errors.New("could not save complete book")
	}
	return nil
}
