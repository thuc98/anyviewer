package main

import (
	"anyviewer-go/internal/controller"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
)

func configPath() string {
	exe, _ := os.Executable()
	cwd, _ := os.Getwd()
	for _, base := range []string{filepath.Dir(exe), filepath.Join(filepath.Dir(exe), ".."), cwd, filepath.Join(cwd, "..")} {
		path := filepath.Join(base, "config", "controller-config.json")
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return filepath.Join("config", "controller-config.json")
}
func openBrowser(url string) {
	var command string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		command, args = "open", []string{url}
	case "windows":
		command, args = "cmd", []string{"/c", "start", url}
	default:
		command, args = "xdg-open", []string{url}
	}
	_ = exec.Command(command, args...).Start()
}
func main() {
	config := flag.String("registration", configPath(), "controller registration/settings JSON")
	dictionary := flag.String("dictionary", "", "optional Zstandard dictionary override (default embedded)")
	host := flag.String("host", "127.0.0.1", "listen host")
	port := flag.Int("port", 8765, "listen port (0 = free port)")
	width := flag.Int("width", 1024, "initial remote screen width")
	height := flag.Int("height", 768, "initial remote screen height")
	block := flag.Int("block-size", 32, "JPEG atlas block size")
	open := flag.Bool("open", false, "open browser")
	flag.Parse()
	if *width < 1 || *width > 8192 || *height < 1 || *height > 8192 || *block < 1 {
		fmt.Fprintln(os.Stderr, "invalid screen dimensions")
		os.Exit(2)
	}
	server, err := controller.NewServer(controller.Options{ConfigPath: *config, DictionaryPath: *dictionary, Width: *width, Height: *height, BlockSize: *block})
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot initialize controller: %v\n", err)
		os.Exit(1)
	}
	listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", *host, *port))
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot listen: %v\n", err)
		os.Exit(1)
	}
	urlHost := *host
	if urlHost == "0.0.0.0" {
		urlHost = "127.0.0.1"
	}
	url := "http://" + net.JoinHostPort(urlHost, fmt.Sprint(listener.Addr().(*net.TCPAddr).Port))
	fmt.Printf("AnyViewer Web running on %s\n", url)
	if *open {
		openBrowser(url)
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() { <-sig; listener.Close() }()
	if err := server.Serve(listener); err != nil && !os.IsNotExist(err) {
		server.Shutdown()
	}
}
