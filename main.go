package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed index.html
var index []byte

type startRequest struct {
	Serial       string `json:"serial"`
	Codec        string `json:"codec"`
	Buffer       int    `json:"buffer"`
	OutputBuffer int    `json:"outputBuffer"`
	BitRate      int    `json:"bitRate"`
}

func (r startRequest) args() ([]string, error) {
	if r.Serial == "" || len(r.Serial) > 256 || strings.ContainsAny(r.Serial, "\r\n\t ") {
		return nil, errors.New("select a device")
	}
	if r.Codec != "raw" && r.Codec != "opus" && r.Codec != "aac" && r.Codec != "flac" {
		return nil, errors.New("unsupported codec")
	}
	if r.Buffer < 1 || r.Buffer > 200 || r.OutputBuffer < 1 || r.OutputBuffer > 50 {
		return nil, errors.New("buffer must be 1–200ms; output buffer 1–50ms")
	}
	args := []string{"--serial=" + r.Serial, "--no-video", "--no-control", "--no-window", "--require-audio", "--verbosity=debug", "--audio-codec=" + r.Codec, fmt.Sprintf("--audio-buffer=%d", r.Buffer), fmt.Sprintf("--audio-output-buffer=%d", r.OutputBuffer)}
	if r.Codec == "opus" || r.Codec == "aac" {
		if r.BitRate == 0 {
			r.BitRate = 128
		}
		if r.BitRate < 32 || r.BitRate > 512 {
			return nil, errors.New("audio bitrate must be 32–512 kbps")
		}
		args = append(args, fmt.Sprintf("--audio-bit-rate=%dK", r.BitRate))
	}
	return args, nil
}

type panel struct {
	mu          sync.Mutex
	cmd         *exec.Cmd
	done        chan struct{}
	stopping    bool
	logs        string
	scrcpy, adb string
}

func (p *panel) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.logs += strings.ToValidUTF8(string(b), "�")
	if len(p.logs) > 65536 {
		p.logs = p.logs[len(p.logs)-65536:]
	}
	return len(b), nil
}

func devices(output string) []string {
	result := []string{}
	for _, line := range strings.Split(output, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[1] == "device" {
			result = append(result, f[0])
		}
	}
	return result
}

func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

type connectRequest struct {
	IP   string `json:"ip"`
	Port int    `json:"port"`
}

func (r connectRequest) address() (string, error) {
	ip := net.ParseIP(strings.TrimSpace(r.IP))
	if ip == nil || r.Port < 1 || r.Port > 65535 {
		return "", errors.New("enter a valid phone IP and port (1–65535)")
	}
	return net.JoinHostPort(ip.String(), strconv.Itoa(r.Port)), nil
}

func (p *panel) connect(w http.ResponseWriter, r *http.Request) {
	var req connectRequest
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	d.DisallowUnknownFields()
	if err := d.Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	address, err := req.address()
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, p.adb, "connect", address).CombinedOutput()
	message := strings.TrimSpace(string(out))
	p.Write([]byte("adb connect " + address + ": " + message + "\n"))
	if err != nil || !(strings.Contains(message, "connected to "+address) || strings.Contains(message, "already connected to "+address)) {
		if message == "" {
			message = fmt.Sprintf("adb connect: %v", err)
		}
		http.Error(w, message, 502)
		return
	}
	reply(w, map[string]string{"serial": address, "message": message})
}

func (p *panel) latency(w http.ResponseWriter, r *http.Request) {
	serial := r.URL.Query().Get("serial")
	if serial == "" || len(serial) > 256 || strings.ContainsAny(serial, "\r\n\t ") {
		http.Error(w, "select a device", 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	var total time.Duration
	for range 5 {
		start := time.Now()
		out, err := exec.CommandContext(ctx, p.adb, "-s", serial, "shell", "echo", "relay-probe").Output()
		elapsed := time.Since(start)
		if err != nil || strings.TrimSpace(string(out)) != "relay-probe" {
			http.Error(w, "ADB probe failed; check device connection", 502)
			return
		}
		total += elapsed
	}
	reply(w, map[string]any{"averageMs": float64(total) / float64(time.Millisecond) / 5, "samples": 5})
}

func (p *panel) start(w http.ResponseWriter, r *http.Request) {
	var req startRequest
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	d.DisallowUnknownFields()
	if err := d.Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	args, err := req.args()
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd != nil {
		http.Error(w, "already running or stopping", 409)
		return
	}
	cmd := exec.Command(p.scrcpy, args...)
	cmd.Env = append(os.Environ(), "ADB="+p.adb)
	cmd.Stdout, cmd.Stderr = p, p
	configureProcess(cmd)
	if err := cmd.Start(); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	p.cmd, p.done = cmd, make(chan struct{})
	p.logs = "Starting: scrcpy " + strings.Join(args, " ") + "\n"
	done := p.done
	go func() {
		err := cmd.Wait()
		p.mu.Lock()
		defer p.mu.Unlock()
		p.logs += fmt.Sprintf("\nProcess exited: %v\n", err)
		p.cmd = nil
		p.stopping = false
		close(done)
	}()
	reply(w, map[string]bool{"started": true})
}

func (p *panel) stop() error {
	p.mu.Lock()
	cmd, done := p.cmd, p.done
	if cmd == nil {
		p.mu.Unlock()
		return nil
	}
	first := !p.stopping
	p.stopping = true
	p.mu.Unlock()
	if first {
		if err := interruptProcess(cmd); err != nil {
			p.Write([]byte("Stop signal: " + err.Error() + "\n"))
		}
	}
	select {
	case <-done:
		return nil
	case <-time.After(4 * time.Second):
		if err := killProcess(cmd); err != nil {
			return err
		}
	}
	select {
	case <-done:
		return nil
	case <-time.After(3 * time.Second):
		return errors.New("process has not exited")
	}
}

func main() {
	addr := flag.String("listen", "127.0.0.1:8080", "HTTP address; use :8080 for trusted LAN access")
	scrcpy := flag.String("scrcpy", "scrcpy", "scrcpy executable")
	adb := flag.String("adb", "adb", "adb executable")
	flag.Parse()
	p := &panel{scrcpy: *scrcpy, adb: *adb}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(index)
	})
	mux.HandleFunc("GET /api/devices", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, p.adb, "devices").Output()
		if err != nil {
			http.Error(w, "adb devices: "+err.Error(), 502)
			return
		}
		reply(w, devices(string(out)))
	})
	mux.HandleFunc("POST /api/start", p.start)
	mux.HandleFunc("POST /api/connect", p.connect)
	mux.HandleFunc("GET /api/latency", p.latency)
	mux.HandleFunc("POST /api/stop", func(w http.ResponseWriter, r *http.Request) {
		if err := p.stop(); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		reply(w, map[string]bool{"stopped": true})
	})
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		reply(w, map[string]any{"running": p.cmd != nil, "stopping": p.stopping, "logs": p.logs})
	})
	server := &http.Server{Addr: *addr, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host {
			http.Error(w, "foreign origin", 403)
			return
		}
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			http.Error(w, "foreign site", 403)
			return
		}
		mux.ServeHTTP(w, r)
	}), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
	ctx, cancel := signal.NotifyContext(context.Background(), shutdownSignals()...)
	defer cancel()
	go func() {
		<-ctx.Done()
		if err := p.stop(); err != nil {
			log.Print(err)
		}
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		server.Shutdown(c)
	}()
	log.Printf("Hello from scrcpy-audio-relay: http://%s", *addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		p.stop()
		log.Fatal(err)
	}
}
