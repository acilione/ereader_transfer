package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var version = "dev"

//go:embed web/*
var embeddedWeb embed.FS

func main() {
	port := flag.Int("port", 8080, "Port to listen on (0 chooses an available port)")
	bind := flag.String("bind", "0.0.0.0", "Listen on all interfaces (0.0.0.0 or ::), or loopback only")
	data := flag.String("data", "", "Library directory (default: pageport-data beside the app)")
	noBrowser := flag.Bool("no-browser", false, "Do not open the desktop browser")
	showVersion := flag.Bool("version", false, "Print the app version")
	flag.Parse()
	if *showVersion {
		fmt.Println("Pageport " + version)
		return
	}
	if *port < 0 || *port > 65535 {
		log.Fatal("Port must be between 0 and 65535")
	}
	bindIP := net.ParseIP(*bind)
	if *bind != "localhost" && (bindIP == nil || (!bindIP.IsUnspecified() && !bindIP.IsLoopback())) {
		log.Fatal("Use --bind 0.0.0.0 (Wi-Fi and desktop), :: (IPv6 and desktop), or a loopback address (desktop only).")
	}
	if *data == "" {
		executable, err := os.Executable()
		if err != nil {
			log.Fatal(err)
		}
		base := filepath.Dir(executable)
		if strings.Contains(base, "go-build") {
			base, err = os.Getwd()
			if err != nil {
				log.Fatal(err)
			}
		}
		*data = filepath.Join(base, "pageport-data")
	}
	assets, err := fs.Sub(embeddedWeb, "web")
	if err != nil {
		log.Fatal(err)
	}
	app, err := newApp(*data, assets)
	if err != nil {
		log.Fatalf("Cannot open library: %v. Use --data to choose a writable directory.", err)
	}
	defer app.Close()
	listener, err := net.Listen("tcp", net.JoinHostPort(*bind, strconv.Itoa(*port)))
	if err != nil {
		log.Fatalf("Cannot start Pageport: %v. Try --port 8081 if this port is in use.", err)
	}
	app.port = listener.Addr().(*net.TCPAddr).Port
	app.bind = *bind
	server := &http.Server{
		Handler:           app,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	localHost := "127.0.0.1"
	if bindIP != nil && bindIP.IsLoopback() {
		localHost = bindIP.String()
	}
	localURL := "http://" + net.JoinHostPort(localHost, strconv.Itoa(app.port))
	fmt.Printf("\nPageport %s\nDesktop: %s\nLibrary: %s\n", version, localURL, app.dataDir)
	for _, address := range app.addresses() {
		fmt.Printf("Kobo:    %s\n", address.URL)
	}
	fmt.Printf("Pairing code: %s\nKeep Pageport running while downloading. Press Ctrl+C to quit.\n\n", app.code)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
		}
	}()
	if !*noBrowser {
		go func() {
			if err := openBrowser(localURL); err != nil {
				log.Printf("Open %s in your browser to use Pageport.", localURL)
			}
		}()
	}
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
	<-shutdownDone
}
