package main

import (
	"strings"
	"testing"
)

func TestMonitoringVerbosity(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		req := startRequest{Serial: "device", Codec: "raw", Buffer: 60, OutputBuffer: 10, MonitorLatency: enabled}
		args, err := req.args()
		if err != nil {
			t.Fatal(err)
		}
		want := "--verbosity=info"
		if enabled {
			want = "--verbosity=verbose"
		}
		if !strings.Contains(strings.Join(args, " "), want) {
			t.Fatalf("monitoring=%v: expected %s in %v", enabled, want, args)
		}
	}
}

func TestAudioSource(t *testing.T) {
	for _, source := range []string{"", "output", "playback"} {
		req := startRequest{Serial: "device", Codec: "raw", AudioSource: source, Buffer: 60, OutputBuffer: 10}
		args, err := req.args()
		if err != nil {
			t.Fatal(err)
		}
		want := "output"
		if source != "" {
			want = source
		}
		if !strings.Contains(strings.Join(args, " "), "--audio-source="+want) {
			t.Fatalf("source=%q: expected %s in %v", source, want, args)
		}
	}
	req := startRequest{Serial: "device", Codec: "raw", AudioSource: "mic", Buffer: 60, OutputBuffer: 10}
	if _, err := req.args(); err == nil {
		t.Fatal("expected unsupported audio source to fail")
	}
}

func TestPipelineLatency(t *testing.T) {
	logs := "VERBOSE: [Audio] Buffering: target=2880 avg=2400.0 cur=2880 compensation=0 (underflow=0)\n" +
		"VERBOSE: [Audio] Buffering: target=2880 avg=2880.0 cur=2880 compensation=0 (underflow=0)\n"
	got := pipelineLatency(logs, 10)
	if got == nil {
		t.Fatal("expected latency, got nil")
	}
	if got["regulatorMs"] != 60 || got["totalMs"] != 70 {
		t.Fatalf("unexpected latency: %#v", got)
	}
	if pipelineLatency("no match", 10) != nil {
		t.Fatal("expected nil without a buffering line")
	}
}

func TestStabilityReports(t *testing.T) {
	clean := "[Audio] Buffering: target=2880 avg=2880.0 cur=2880 compensation=0 (underflow=0)\n"
	dropout := "[Audio] Buffering: target=2880 avg=2880.0 cur=2880 compensation=0 (underflow=480)\n"
	latency := pipelineLatency(dropout+strings.Repeat(clean, 9), 10)
	if latency["underflowMs"] != 10 || latency["reports"] != 10 || latency["targetMs"] != 60 {
		t.Fatalf("unexpected report window: %#v", latency)
	}
	if !strings.Contains(stabilityAdvice(latency).Advice, "Try target 70 ms / output 10 ms") {
		t.Fatal(stabilityAdvice(latency))
	}
	latency = pipelineLatency(dropout+strings.Repeat(clean, 10), 10)
	if latency["underflowMs"] != 0 || !strings.Contains(stabilityAdvice(latency).Advice, "No underruns") {
		t.Fatalf("old underrun should leave the window: %#v", latency)
	}
	if stabilityAdvice(pipelineLatency(clean, 10)).State != "checking" {
		t.Fatal("should wait for more reports before reporting no underruns")
	}
	if !strings.Contains(stabilityAdvice(nil).Advice, "Waiting") {
		t.Fatal("missing reports must not indicate stability")
	}
	for _, target := range []float64{195, 200} {
		advice := stabilityAdvice(map[string]float64{"targetMs": target, "underflowMs": 10, "reports": 10, "outputMs": 10})
		want := "Try target 200 ms / output 10 ms"
		if target == 200 {
			want = "already at the maximum"
		}
		if !strings.Contains(advice.Advice, want) {
			t.Fatal(advice)
		}
	}
}

func TestStabilityStatesAndPairs(t *testing.T) {
	for _, tc := range []struct {
		name, state, pair                          string
		reports, silence, affected, target, output float64
	}{
		{"warming up", "checking", "target 60 ms / output 10 ms", 4, 0, 0, 60, 10},
		{"stable", "stable", "target 60 ms / output 5 ms", 10, 0, 0, 60, 5},
		{"occasional", "unstable", "target 70 ms / output 10 ms", 10, 10, 1, 60, 5},
		{"large dropout", "poor", "target 90 ms / output 20 ms", 10, 100, 1, 60, 10},
		{"frequent", "poor", "target 90 ms / output 20 ms", 10, 30, 5, 60, 10},
		{"preserve output and cap target", "poor", "target 200 ms / output 40 ms", 10, 100, 1, 195, 40},
		{"max buffers", "poor", "already at the maximum", 10, 100, 1, 200, 50},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := stabilityAdvice(map[string]float64{"reports": tc.reports, "underflowMs": tc.silence, "affectedReports": tc.affected, "targetMs": tc.target, "outputMs": tc.output})
			if got.State != tc.state || !strings.Contains(got.Advice, tc.pair) {
				t.Fatalf("unexpected status: %+v", got)
			}
		})
	}
}
