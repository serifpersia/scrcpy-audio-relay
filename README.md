# scrcpy-audio-relay

Small standard-library-only Go orchestration panel. No audio decoding, playback,
JAR embedding, or protocol implementation in Go. Requires Go 1.22+ to build.

```
scrcpy-audio-relay/
  go.mod
  main.go
  process_unix.go
  process_windows.go
  index.html
  README.md
```

## Windows

From this directory, in PowerShell:

```powershell
$env:CGO_ENABLED='0'
go build -trimpath -o scrcpy-audio-relay.exe .
./scrcpy-audio-relay.exe
./scrcpy-audio-relay.exe -scrcpy C:/tools/scrcpy/scrcpy.exe -adb C:/tools/scrcpy/adb.exe
```

Open http://127.0.0.1:8080. For trusted LAN access, add `-listen :8080`.
The LAN panel has no authentication; everyone with access can control the link.
Cross-origin browser requests are rejected. Do not expose it to the internet.

## Alpine Linux / RAM disk

```sh
CGO_ENABLED=0 go build -trimpath -o scrcpy-audio-relay .
./scrcpy-audio-relay
```

Cross-compile from PowerShell for a typical x86-64 Alpine laptop:

```powershell
$env:CGO_ENABLED='0'
$env:GOOS='linux'
$env:GOARCH='amd64'
go build -trimpath -o scrcpy-audio-relay .
Remove-Item Env:GOOS, Env:GOARCH
```

Only the Go panel is standalone/static. Install the official scrcpy package,
its matching server JAR, adb, and native audio libraries separately on Alpine.
Configure a working host audio output before streaming. Diskless installations
must restore these packages/configuration at boot using their usual Alpine setup.

## Command and latency

Default UI settings launch:

```sh
scrcpy --serial=SERIAL --no-video --no-control --no-window --require-audio --verbosity=debug --audio-codec=raw --audio-buffer=60 --audio-output-buffer=10
```

`--no-video` disables video capture; `--no-control` disables device control.
`--no-window` prevents scrcpy from opening an SDL window while keeping audio playback.
Do not use `--no-playback`: that disables audio playback too. No recorder is
started. Raw PCM avoids the encoder; Opus is available as an alternative.
The 60ms target plus 10ms output buffer does not measure total latency or
guarantee under 60ms: capture, USB, scheduling and the sound device add delay.
Measure on the actual laptop/phone, and raise buffers if underruns occur.
Debug stdout/stderr is bounded to roughly 64KiB and polled once per second;
it reports scrcpy diagnostics, not a measured end-to-end latency meter.

Android 11+ is required; unlock Android 11 before starting. Use a recent official
scrcpy release supporting these flags; older releases fail visibly in the log.
Reference: https://github.com/Genymobile/scrcpy/blob/master/doc/audio.md

## Lifecycle and protocol ownership

`exec.Command` starts scrcpy directly, without a shell. Stop sends SIGINT on
Unix, or CTRL_BREAK_EVENT to a dedicated Windows process group. After four
seconds it force-kills the Unix process group or Windows process tree and waits
for exit. Windows graceful signaling requires a console; tree-kill is the
fallback. Normal app shutdown also stops playback. Forced app termination
cannot run shutdown handlers.

scrcpy owns its adb children and Android server. It pushes the matching server
to `/data/local/tmp/scrcpy-server.jar`, then runs Android `app_process` with
CLASSPATH pointing to that JAR. Server options are arguments to `app_process`,
not arguments to `adb push`. The client generates a session ID (SCID), uses
`scrcpy_<8 hex digits>` as its socket name, sets up adb reverse (forward as
fallback), and removes the tunnel after connecting. Video/control are disabled
in the server arguments too. Leave default cleanup enabled: do not pass
`--no-cleanup`. The official client/server handle connection closure and cleanup.

Implementation references are in the official
[scrcpy source](https://github.com/Genymobile/scrcpy), particularly
`app/src/server.c`, `app/src/scrcpy.c`, `app/src/adb/adb_tunnel.c`, and
`server/src/main/java/com/genymobile/scrcpy/CleanUp.java`.
No `adb kill-server` is issued: the adb daemon is shared with other tools and
devices. Abrupt forced termination can bypass scrcpy cleanup; this starter does
not promise transactional cleanup after crashes or power loss.

Use the Phone IP and ADB port fields and press Connect to run `adb connect`.
The phone must already accept ADB TCP connections. A successful connection
refreshes the device dropdown and selects that IP:port. Errors appear in the UI
and console. No pairing or QR workflow is included.

API: POST `/api/connect` takes `{"ip":"192.168.1.100","port":5555}`.
GET `/api/devices` returns ready serials, POST `/api/start` takes
`{"serial":"SERIAL","codec":"opus","buffer":60,"outputBuffer":10,"bitRate":128}`,
POST `/api/stop` stops the session, GET `/api/status` returns running/stopping/logs.
One session at a time; stop before changing device or settings.

The Measure ADB round trip button times five `adb -s SERIAL shell echo relay-probe`
commands and reports their mean via GET `/api/latency?serial=SERIAL`.
This includes host process startup, ADB transport, shell startup, and scheduling;
it is neither pure network RTT nor end-to-end audio latency. It runs only on
request so continuous probes do not add load during normal playback.

The default target buffer is 60ms; output is 10ms. Lower the target in 10ms
steps if playback stays clean. Keep output at 10ms initially, then optionally try
5ms once playback is stable. No fixed ratio is required. Restart streaming to
apply changes. SDL may not honor the exact requested output block size.
The dashboard estimates the one-way buffer contribution as target + requested
output buffer (60 + 10 = approximately 70ms), plus unknown pipeline delay.
This is a nominal budget, not a measured total or guaranteed lower bound.
It excludes capture, codec, transport, and host audio hardware delay; the ADB
command round trip is not used to guess those values.
Scrcpy's `app/src/audio_regulator.c` maintains average queued samples near the target,
inserts silence on underrun, and compensates gradually. Its verbose buffering
logs expose queue occupancy, not total audio latency.

Bitrate selection is available for Opus and AAC, from 32 to 512 kbps (128 default).
The API accepts `bitRate` in kbps and passes `--audio-bit-rate=VALUEK` to scrcpy.
Omitting it uses 128 kbps. PCM has a fixed format bitrate; FLAC is lossless and
variable, so the bitrate selector stays visible but disabled for those codecs.

## Release downloads

Prebuilt Windows x64 and Linux x64 binaries are available in
[GitHub Releases](https://github.com/serifpersia/scrcpy-audio-relay/releases/latest).
They contain the dashboard, but require scrcpy and adb installed separately.

For Alpine x86_64, with the community repository enabled:

```sh
apk add --no-cache scrcpy android-tools
wget -O /usr/local/bin/scrcpy-audio-relay https://github.com/serifpersia/scrcpy-audio-relay/releases/latest/download/scrcpy-audio-relay-linux-amd64
chmod +x /usr/local/bin/scrcpy-audio-relay
scrcpy-audio-relay -listen :8080
```

Release assets also include `SHA256SUMS.txt`. The Linux binary is built with
CGO disabled and requires no libc. The latest download URL stays the same
across releases; use a versioned release URL to pin a deployment.
