//go:build linux

package main

import "testing"

func TestAudioDevices(t *testing.T) {
	output := "card 0: PCH [HDA Intel PCH], device 0: ALC295 Analog [ALC295 Analog]\n" +
		"card 0: PCH [HDA Intel PCH], device 3: HDMI 0 [HDMI 0]\n" +
		"card 1: Device [USB Audio Device], device 0: USB Audio [USB Audio]\n"
	got := parseAplay(output)
	if len(got) != 3 {
		t.Fatalf("expected 3 devices: %#v", got)
	}
	if got[0].ID != "plughw:CARD=PCH,DEV=0" || got[1].ID != "plughw:CARD=PCH,DEV=3" || got[2].ID != "plughw:CARD=Device,DEV=0" {
		t.Fatalf("unexpected device ids: %#v", got)
	}
	for device, want := range map[string]string{
		"":                          "",
		"default":                   "",
		"plughw:CARD=PCH,DEV=3":     "PCH",
		"sysdefault:CARD=USB-Audio": "USB-Audio",
	} {
		got, err := audioCard(device)
		if err != nil || got != want {
			t.Fatalf("audioCard(%q) = %q, %v; want %q", device, got, err, want)
		}
	}
	if _, err := audioCard("plughw:DEV=0"); err == nil {
		t.Fatal("expected device without CARD= to fail")
	}
}

func TestAudioControlMuteSupport(t *testing.T) {
	for _, tc := range []struct {
		output  string
		muted   bool
		canMute bool
	}{
		{"Simple mixer control 'Master',0\n  Playback 80 [80%] [on]\n", false, true},
		{"Simple mixer control 'Master',0\n  Playback 0 [0%] [off]\n", true, true},
		{"Simple mixer control 'PCM',0\n  Playback 80 [80%] [80.00dB]\n", false, false},
	} {
		got := parseAudioControl(tc.output, "test")
		if got.Muted != tc.muted || got.CanMute != tc.canMute {
			t.Fatalf("output %q: got %+v", tc.output, got)
		}
	}
}
