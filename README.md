# scrcpy-audio-relay

A lightweight web dashboard for native [scrcpy](https://github.com/Genymobile/scrcpy) audio playback. Built with Go and an embedded, mobile-friendly UI. Audio plays on the host computer, without a scrcpy window.

## Get started

Install scrcpy and ADB, then download the Windows or Linux x64 binary from [Releases](https://github.com/serifpersia/scrcpy-audio-relay/releases/latest). Android 11+ is required; unlock Android 11 before connecting.

Run the binary and open **http://127.0.0.1:8080**. Select a connected device, or enter its IP and ADB port, then start audio. Network ADB must already be enabled and authorized.

Use `-scrcpy` and `-adb` to specify executable paths. Use `-listen :8080` for trusted LAN access; the dashboard has no authentication.

On Alpine, `start-scrcpy-relay.sh` installs the dependencies, downloads the latest release, and starts the dashboard in the background.

## Audio controls

- Raw PCM, Opus, AAC and FLAC codecs; bitrate selection for Opus/AAC.
- **Output** source (default) forwards the whole device mix and mutes the phone speaker. **Playback** source captures app playback while the phone keeps playing. Playback requires Android 13+ and apps may opt out.
- Select the host output device and adjust its volume from the dashboard; changes are applied at the next scrcpy start. On Windows the selected device becomes the system default output. On Linux the ALSA device is passed to SDL3 (requires scrcpy 4.x/SDL3) and volume uses `alsa-utils` (`aplay`, `amixer`).
- Target and output buffers default to **60ms / 10ms**. Restart audio to apply changes.
- Session logs, optional live host-pipeline latency monitoring (scrcpy audio queue + requested output buffer), and an on-demand ADB round-trip check. Enable monitoring before starting audio to use verbose logging; otherwise logging stays at info level.
- With monitoring enabled, playback stability shows green (stable), amber (unstable), or red (poor) based on the last 10 received audio reports, and suggests a target/output buffer pair to test after restarting. Suggestions are heuristics, not a measured optimum; reports cannot identify the cause or detect every audible problem.

Lower buffers reduce nominal buffering but can cause dropouts. The buffer estimate and ADB round trip do not measure end-to-end audio latency.

## Build

Requires Go 1.22+. Set `CGO_ENABLED=0` for a standalone binary:

```sh
CGO_ENABLED=0 go build -trimpath -o scrcpy-audio-relay .
```

On Windows PowerShell:

```powershell
$env:CGO_ENABLED='0'
go build -trimpath -o scrcpy-audio-relay.exe .
```

Scrcpy and ADB remain separate runtime requirements.

## License

This project is licensed under the [MIT License](LICENSE). Scrcpy and ADB retain their respective licenses.
