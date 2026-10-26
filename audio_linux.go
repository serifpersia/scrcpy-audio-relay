//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	aplayLine        = regexp.MustCompile(`^card (\d+): (\S+) \[(.*?)\], device (\d+): (.*?) \[(.*?)\]`)
	mixerControlLine = regexp.MustCompile(`^Simple mixer control '(.+)',\d+$`)
	percentLine      = regexp.MustCompile(`\[(\d+)%\]`)
	cardLine         = regexp.MustCompile(`(?:^|:)CARD=([^,]+)`)
	muteSwitchLine   = regexp.MustCompile(`\[(on|off)\]`)
	playbackMuteLine = regexp.MustCompile(`(?m)^\s*(?:Mono|Front Left|Front Right): Playback.*\[(on|off)\]`)
)

func parseAplay(output string) []audioDevice {
	result := []audioDevice{}
	for _, line := range strings.Split(output, "\n") {
		m := aplayLine.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		result = append(result, audioDevice{
			ID:    fmt.Sprintf("plughw:CARD=%s,DEV=%s", m[2], m[4]),
			Label: fmt.Sprintf("%s — %s", m[3], m[5]),
		})
	}
	return result
}

func audioDevices() []audioDevice {
	result := []audioDevice{}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "aplay", "-l").Output()
	if err != nil {
		return result
	}
	return append(result, parseAplay(string(out))...)
}

func audioCard(device string) (string, error) {
	if device == "" || device == "default" {
		return "", nil
	}
	if len(device) > 512 || strings.ContainsAny(device, "\r\n\t ") {
		return "", errors.New("invalid audio device")
	}
	m := cardLine.FindStringSubmatch(device)
	if m == nil || len(m[1]) > 64 || strings.ContainsAny(m[1], "\r\n\t ") {
		return "", errors.New("audio device has no mixer card")
	}
	return m[1], nil
}

func amixerArgs(card string, args ...string) []string {
	if card == "" {
		return args
	}
	return append([]string{"-c", card}, args...)
}

func audioControlValue(card, name string) (audioControl, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "amixer", amixerArgs(card, "sget", name)...).Output()
	if err != nil {
		return audioControl{}, false
	}
	return parseAudioControl(string(out), name), true
}

func parseAudioControl(text, name string) audioControl {
	m := percentLine.FindStringSubmatch(text)
	if m == nil || !strings.Contains(text, "Playback") {
		return audioControl{}
	}
	volume, _ := strconv.Atoi(m[1])
	state := playbackMuteLine.FindStringSubmatch(text)
	canMute := strings.Contains(text, "pswitch") && state != nil
	muted := canMute && state[1] == "off"
	return audioControl{Name: name, Volume: volume, Muted: muted, CanMute: canMute}
}

func audioControls(device string) []audioControl {
	card, err := audioCard(device)
	if err != nil {
		return []audioControl{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "amixer", amixerArgs(card, "scontrols")...).Output()
	if err != nil {
		return []audioControl{}
	}
	result := []audioControl{}
	for _, line := range strings.Split(string(out), "\n") {
		m := mixerControlLine.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		if control, ok := audioControlValue(card, m[1]); ok {
			result = append(result, control)
		}
	}
	return result
}

func setAudioVolume(device, control string, volume int) error {
	card, err := audioCard(device)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "amixer", amixerArgs(card, "sset", control, fmt.Sprintf("%d%%", volume))...).CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(out))
		if message == "" {
			message = err.Error()
		}
		return errors.New(message)
	}
	exec.CommandContext(ctx, "amixer", amixerArgs(card, "sset", control, "unmute")...).Run()
	return nil
}

func setAudioMute(device, control string, muted bool) error {
	card, err := audioCard(device)
	if err != nil {
		return err
	}
	action := "unmute"
	if muted {
		action = "mute"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "amixer", amixerArgs(card, "sset", control, action)...).CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(out))
		if message == "" {
			message = err.Error()
		}
		return errors.New(message)
	}
	return nil
}

func applyAudioDevice(device string) error { return nil }

func audioEnv(device string) []string {
	if device == "" || device == "default" {
		return nil
	}
	return []string{"SDL_AUDIODRIVER=alsa", "SDL_AUDIO_ALSA_DEFAULT_DEVICE=" + device, "SDL_AUDIO_ALSA_DEFAULT_PLAYBACK_DEVICE=" + device}
}
