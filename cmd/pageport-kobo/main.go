//go:build linux

// pageport-kobo uses NickelDBus's native dialogs; it never opens a browser.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"pageport/internal/direct"
	"pageport/internal/receiver"
)

type nickel struct{ command string }

func (n nickel) call(ctx context.Context, args ...string) (string, error) {
	output, err := exec.CommandContext(ctx, n.command, args...).Output()
	return strings.TrimSpace(string(output)), err
}
func (n nickel) Confirm(ctx context.Context, j direct.Job) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute+5*time.Second)
	defer cancel()
	names := []string{}
	for i, f := range j.Files {
		if i == 5 {
			names = append(names, "…")
			break
		}
		names = append(names, html.EscapeString(f.Name))
	}
	body := fmt.Sprintf("Receive %d book(s) from your paired Pageport computer?<br/><br/>%s", len(j.Files), strings.Join(names, "<br/>"))
	output, err := n.call(ctx, "-t", "180000", "-s", "dlgConfirmResult", "-m", "dlgConfirmAcceptReject", "Pageport: incoming books", body, "Accept", "Reject")
	if err != nil {
		cleanup, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		_, _ = n.call(cleanup, "-m", "dlgConfirmClose")
		return false, err
	}
	// qndb 0.2.0 prints the signal name followed by its numeric result.
	if output == "dlgConfirmResult 1" {
		return true, nil
	}
	if output == "dlgConfirmResult 0" {
		return false, nil
	}
	return false, errors.New("unexpected confirmation response")
}
func (n nickel) Import(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 125*time.Second)
	defer cancel()
	_, err := n.call(ctx, "-t", "120000", "-s", "pfmDoneProcessing", "-m", "pfmRescanBooksFull")
	return err
}

func main() {
	executable, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	configPath := flag.String("config", filepath.Join(filepath.Dir(executable), "config.json"), "Paired receiver configuration")
	flag.Parse()
	if _, err = os.Stat("/mnt/onboard/.kobo/version"); err != nil {
		log.Fatal("This receiver must run on a Kobo with onboard storage mounted.")
	}
	lock, err := os.OpenFile("/tmp/pageport-receiver.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		log.Fatal(err)
	}
	defer lock.Close()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		log.Fatal("Pageport receiver is already running.")
	}
	command, err := exec.LookPath("qndb")
	if err != nil {
		log.Fatal("Install NickelDBus 0.2.0 or later first (qndb is missing).")
	}
	n := nickel{command}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	check, cancel := context.WithTimeout(ctx, 10*time.Second)
	firmware, err := n.call(check, "-m", "ndbFirmwareVersion")
	if err != nil || !strings.HasPrefix(firmware, "4.") {
		cancel()
		log.Fatal("This experimental receiver targets Kobo firmware 4.x.")
	}
	connected, err := n.call(check, "-m", "ndbSignalConnected", "pfmAboutToConnect")
	cancel()
	if err != nil || (connected != "1" && connected != "true") {
		log.Fatal("USB connection monitoring is unavailable; receiver will not start.")
	}
	data, err := os.ReadFile(*configPath)
	if err != nil {
		log.Fatal("Download config.json from Pageport and place it beside the receiver.")
	}
	var config direct.Config
	if json.Unmarshal(data, &config) != nil {
		log.Fatal("Invalid config.json.")
	}
	c, err := receiver.New(config, "/mnt/onboard/Pageport", filepath.Join(filepath.Dir(*configPath), "receipts"), n)
	if err != nil {
		log.Fatal(err)
	}
	// Stop promptly before Nickel exposes onboard storage to the USB host.
	go func() { _, _ = n.call(ctx, "-s", "pfmAboutToConnect"); stop() }()
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = c.Heartbeat(ctx)
			}
		}
	}()
	log.Print("Pageport receiver running. Keep Kobo awake and connected to Wi-Fi.")
	for ctx.Err() == nil {
		j, err := c.Poll(ctx)
		if err == nil && j != nil {
			err = c.Process(ctx, *j)
		}
		if err != nil && ctx.Err() == nil {
			log.Print(err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(4 * time.Second):
		}
	}
	log.Print("Receiver stopped; restart from NickelMenu after USB or reboot.")
}
