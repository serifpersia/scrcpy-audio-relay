//go:build !windows && !linux

package main

import "errors"

func audioDevices() []audioDevice { return []audioDevice{} }

func audioControls(string) []audioControl { return []audioControl{} }

func setAudioVolume(string, string, int) error {
	return errors.New("audio controls are not supported on this platform")
}

func applyAudioDevice(string) error { return nil }

func audioEnv(string) []string { return nil }
