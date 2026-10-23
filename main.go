// Package main is the entry point for the Crush CLI.
package main

import (
	"log/slog"
	"net"
	"net/http"
	_ "net/http/pprof"
	"strings"

	"github.com/charmbracelet/crush/internal/cmd"
	_ "github.com/charmbracelet/crush/internal/dns"
	"github.com/charmbracelet/crush/internal/env"
	_ "github.com/joho/godotenv/autoload"
)

func main() {
	if setting := env.Getenv("KLASH_PROFILE"); setting != "" {
		go servePprof(setting)
	}

	cmd.Execute()
}

// pprofDefaultAddr is where KLASH_PROFILE=1 serves pprof.
const pprofDefaultAddr = "localhost:6060"

// servePprof serves net/http/pprof for KLASH_PROFILE; see pprofListen.
func servePprof(setting string) {
	ln, err := pprofListen(setting, pprofDefaultAddr)
	if err != nil {
		slog.Error("Failed to pprof listen", "error", err)
		return
	}
	slog.Info("Serving pprof", "address", ln.Addr().String())
	if err := http.Serve(ln, nil); err != nil {
		slog.Error("pprof server stopped", "error", err)
	}
}

// pprofListen opens the listener pprof is served on: the host:port that
// setting names, or else defaultAddr, or, when another process already
// holds defaultAddr, a free port on localhost. Every crash on a machine
// asked for the same port, so only the first could be profiled; a klash
// that hung later could not be. Its port is in its log, or in
// `ss -ltnp | grep pid=<pid>`.
func pprofListen(setting, defaultAddr string) (net.Listener, error) {
	if strings.Contains(setting, ":") {
		return net.Listen("tcp", setting)
	}
	ln, err := net.Listen("tcp", defaultAddr)
	if err == nil {
		return ln, nil
	}
	return net.Listen("tcp", "localhost:0")
}
