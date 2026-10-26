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
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed index.html
var index []byte

type startRequest struct {
	Serial         string `json:"serial"`
	Codec          string `json:"codec"`
	AudioSource    string `json:"audioSource"`
	AudioDevice    string `json:"audioDevice"`
	Buffer         int    `json:"buffer"`
	OutputBuffer   int    `json:"outputBuffer"`
	BitRate        int    `json:"bitRate"`
	MonitorLatency bool   `json:"monitorLatency"`
}

func (r startRequest) args() ([]string, error) {
	if r.Serial == "" || len(r.Serial) > 256 || strings.ContainsAny(r.Serial, "\r\n\t ") {
		return nil, errors.New("select a device")
	}
	if r.Codec != "raw" && r.Codec != "opus" && r.Codec != "aac" && r.Codec != "flac" {
		return nil, errors.New("unsupported codec")
	}
	source := r.AudioSource
	if source == "" {
		source = "output"
	}
	if source != "output" && source != "playback" {
		return nil, errors.New("unsupported audio source")
	}
	if len(r.AudioDevice) > 512 || strings.ContainsAny(r.AudioDevice, "\r\n\t") {
		return nil, errors.New("invalid audio device")
	}
	if r.Buffer < 1 || r.Buffer > 200 || r.OutputBuffer < 1 || r.OutputBuffer > 50 {
		return nil, errors.New("buffer must be 1–200ms; output buffer 1–50ms")
	}
	verbosity := "info"
	if r.MonitorLatency {
		verbosity = "verbose"
	}
	args := []string{"--serial=" + r.Serial, "--no-video", "--no-control", "--no-window", "--require-audio", "--verbosity=" + verbosity, "--audio-source=" + source, "--audio-codec=" + r.Codec, fmt.Sprintf("--audio-buffer=%d", r.Buffer), fmt.Sprintf("--audio-output-buffer=%d", r.OutputBuffer)}
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
	mu             sync.Mutex
	cmd            *exec.Cmd
	done           chan struct{}
	stopping       bool
	logs           string
	scrcpy, adb    string
	outputBuffer   int
	monitorLatency bool
}

const audioSampleRate = 48000

var bufferingLine = regexp.MustCompile(`\[Audio\] Buffering: target=(\d+) avg=([\d.]+) cur=(\d+) compensation=(-?\d+) \(underflow=(\d+)\)`)

func pipelineLatency(logs string, outputBufferMs int) map[string]float64 {
	matches := bufferingLine.FindAllStringSubmatch(logs, -1)
	if len(matches) == 0 {
		return nil
	}
	last := matches[len(matches)-1]
	avg, _ := strconv.ParseFloat(last[2], 64)
	cur, _ := strconv.ParseFloat(last[3], 64)
	regulator := avg / audioSampleRate * 1000
	recent := matches
	if len(recent) > 10 {
		recent = recent[len(recent)-10:]
	}
	var underflow, affected float64
	for _, report := range recent {
		samples, _ := strconv.ParseFloat(report[5], 64)
		underflow += samples
		if samples > 0 {
			affected++
		}
	}
	target, _ := strconv.ParseFloat(last[1], 64)
	return map[string]float64{
		"targetMs":        target / audioSampleRate * 1000,
		"reports":         float64(len(recent)),
		"underflowMs":     underflow / audioSampleRate * 1000,
		"affectedReports": affected,
		"regulatorMs":     regulator,
		"currentMs":       cur / audioSampleRate * 1000,
		"outputMs":        float64(outputBufferMs),
		"totalMs":         regulator + float64(outputBufferMs),
	}
}

type stabilityStatus struct {
	State  string `json:"state"`
	Label  string `json:"label"`
	Advice string `json:"advice"`
}

func stabilityAdvice(latency map[string]float64) stabilityStatus {
	if latency == nil {
		return stabilityStatus{"checking", "Checking", "Waiting for audio reports…"}
	}
	reports := int(latency["reports"])
	target, output := int(latency["targetMs"]), int(latency["outputMs"])
	silence := latency["underflowMs"]
	if silence > 0 {
		state, label, step := "unstable", "Unstable", 10
		// shortcut: severity thresholds are heuristics; calibrate with real playback measurements.
		if silence >= 100 || (latency["affectedReports"] >= 3 && latency["affectedReports"]*2 >= float64(reports)) {
			state, label, step = "poor", "Poor", 30
			output = max(output, 20)
		} else {
			output = max(output, 10)
		}
		nextTarget := min(target+step, 200)
		advice := fmt.Sprintf("%.1f ms of inserted silence in the last %d received reports. Try target %d ms / output %d ms, then restart audio. More buffering adds delay.", silence, reports, nextTarget, output)
		if target == 200 && output == int(latency["outputMs"]) {
			advice = fmt.Sprintf("%.1f ms of inserted silence in the last %d received reports. Current target %d ms / output %d ms: target is already at the maximum. Buffer changes alone may not fix this.", silence, reports, target, output)
		}
		return stabilityStatus{state, label, advice + " If dropouts persist, check PC load and try USB to compare against Wi-Fi."}
	}
	if reports < 5 {
		return stabilityStatus{"checking", "Checking", fmt.Sprintf("%d/5 received reports without underruns. Current target %d ms / output %d ms; waiting for more reports before recommending this pair.", reports, target, output)}
	}
	return stabilityStatus{"stable", "Stable", fmt.Sprintf("No underruns in the last %d received reports. Recommended pair: keep target %d ms / output %d ms if playback sounds good.", reports, target, output)}
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

type audioDevice struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type audioControl struct {
	Name    string `json:"name"`
	Volume  int    `json:"volume"`
	Muted   bool   `json:"muted"`
	CanMute bool   `json:"canMute"`
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

func (p *panel) audio(w http.ResponseWriter, r *http.Request) {
	device := r.URL.Query().Get("device")
	if len(device) > 512 || strings.ContainsAny(device, "\r\n\t") {
		http.Error(w, "invalid audio device", 400)
		return
	}
	reply(w, map[string]any{"devices": audioDevices(), "controls": audioControls(device)})
}

type volumeRequest struct {
	Device  string `json:"device"`
	Control string `json:"control"`
	Volume  int    `json:"volume"`
	Muted   *bool  `json:"muted"`
}

func (p *panel) volume(w http.ResponseWriter, r *http.Request) {
	var req volumeRequest
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	d.DisallowUnknownFields()
	if err := d.Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if len(req.Device) > 512 || strings.ContainsAny(req.Device, "\r\n\t") {
		http.Error(w, "invalid audio device", 400)
		return
	}
	if req.Control == "" || len(req.Control) > 64 || strings.ContainsAny(req.Control, "\r\n") {
		http.Error(w, "invalid mixer control", 400)
		return
	}
	if req.Muted == nil && (req.Volume < 0 || req.Volume > 100) {
		http.Error(w, "volume must be 0–100%", 400)
		return
	}
	var err error
	if req.Muted != nil {
		err = setAudioMute(req.Device, req.Control, *req.Muted)
	} else {
		err = setAudioVolume(req.Device, req.Control, req.Volume)
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	reply(w, map[string]bool{"ok": true})
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
	if err := applyAudioDevice(req.AudioDevice); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	cmd := exec.Command(p.scrcpy, args...)
	cmd.Env = append(os.Environ(), "ADB="+p.adb)
	cmd.Env = append(cmd.Env, audioEnv(req.AudioDevice)...)
	cmd.Stdout, cmd.Stderr = p, p
	configureProcess(cmd)
	if err := cmd.Start(); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	p.cmd, p.done = cmd, make(chan struct{})
	p.outputBuffer = req.OutputBuffer
	p.monitorLatency = req.MonitorLatency
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
	mux.HandleFunc("GET /api/audio", p.audio)
	mux.HandleFunc("POST /api/audio/volume", p.volume)
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
		var latency map[string]float64
		if p.cmd != nil && p.monitorLatency {
			latency = pipelineLatency(p.logs, p.outputBuffer)
		}
		reply(w, map[string]any{"running": p.cmd != nil, "stopping": p.stopping, "logs": p.logs, "latency": latency, "monitorLatency": p.monitorLatency, "stability": stabilityAdvice(latency)})
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
