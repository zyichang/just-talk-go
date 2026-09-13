# Just Talk

[中文](README.md) · [Website](https://whoamihappyhacking.github.io/just-talk-go/)

Just Talk is a desktop voice input tool. It records audio with a global hotkey, sends it to streaming ASR, and then copies the recognized text to the clipboard or submits it directly into the focused input field.

It is built for people who want to type less and speak more while coding, chatting, writing notes, or working with long text.

## Screenshot

![Just Talk TUI](docs/screenshot-tui.png)

## Features

- Global hotkey recording with `toggle` and `hold` modes.
- Voice hotkeys are limited to keys suitable for global shortcuts: modifiers, function keys, Tab, CapsLock, arrow/navigation keys, and similar non-text keys. Letters, digits, punctuation, Space, and other text-producing keys are rejected.
- Doubao streaming ASR with optimized bidirectional streaming and second-pass recognition.
- Clipboard copy and automatic text submission.
- Always-on-top recording status overlay for Wayland, X11, macOS, and Windows.
- TUI configuration for hotkeys, mode, auto-submit, stop delay, hotwords, and related settings.
- ASR hotwords for project names, people names, English terms, and domain-specific vocabulary.
- Usage statistics for total sessions, total recognized characters, average speed, and recent speed.

## Platform Status

Linux, macOS, and Windows desktops are supported:

| Platform | Status | Notes |
| --- | --- | --- |
| Linux Wayland | Supported | Works with Sway / wlroots; hotkeys use evdev and require input permissions |
| Linux X11 | Supported | Uses native X11 global hotkeys |
| macOS | Supported | Global hotkeys use CGEventTap, recording uses CoreAudio, clipboard uses NSPasteboard, and overlay uses AppKit NSPanel |
| Windows 10/11 | Supported | Global key polling with a low-level keyboard-hook edge fallback, WinMM recording, Unicode clipboard, SendInput auto-submit, and a Win32 status overlay |

## Build

Just Talk uses native platform APIs. Linux and macOS builds require cgo; Windows uses direct Win32 calls from Go and does not require cgo.

Linux build dependencies:

```bash
# Arch Linux
sudo pacman -S --needed go gcc libx11 libxtst libxext wayland

# Debian / Ubuntu
sudo apt install golang-go build-essential libx11-dev libxtst-dev libxext-dev libxinerama-dev libwayland-dev
```

macOS build dependencies:

```bash
# Apple Command Line Tools provide clang and the macOS SDK. Full Xcode is not required.
xcode-select --install
```

Windows build dependency:

```powershell
# Install Go 1.25 or later. No ffmpeg, SoX, or C compiler is required.
winget install --id GoLang.Go --exact
```

Build for the current platform:

```bash
cd just-talk-go
CGO_ENABLED=1 go build -o build/just-talk ./cmd/just-talk
```

Windows PowerShell:

```powershell
cd just-talk-go
go build -o build\just-talk.exe .\cmd\just-talk
```

Install to `~/.local/bin/just-talk`:

```bash
# Make sure ~/.local/bin is in PATH. If not, add this line to ~/.bashrc or ~/.zshrc.
# export PATH="$HOME/.local/bin:$PATH"
build/just-talk --install
# or
make install
```

macOS must be built on macOS. The project does not provide a non-cgo build.

Install on Windows to `%LOCALAPPDATA%\Programs\Just Talk\just-talk.exe`:

```powershell
.\build\just-talk.exe --install
# If the directory is not already in PATH, follow the note printed by the command.
```

## Release Downloads

GitHub Releases provide prebuilt archives for:

- Linux amd64 / arm64
- macOS Intel / Apple Silicon
- Windows amd64 / arm64
- `SHA256SUMS.txt` checksum verification

The release workflow uses GoReleaser v2 with the official `goreleaser/goreleaser-action`. Linux, macOS, and Windows binaries are built natively on matching GitHub-hosted runners. Maintainers can build and publish a release by pushing a `v*` tag, for example:

```bash
git tag v0.7.0
git push origin v0.7.0
```

## Usage

Start the TUI:

```bash
just-talk
```

Run without the TUI:

```bash
just-talk --no-tui
```

Force a backend:

```bash
just-talk --backend wayland
just-talk --backend x11
```

Windows selects its native backend automatically. Check the microphone and configuration before first use:

```powershell
.\build\just-talk.exe --doctor
```

## Configuration

Default config path:

```text
# Linux / macOS
~/.config/just-talk/config.toml

# Windows
%APPDATA%\just-talk\config.toml
```

Recommended hotkey config:

```toml
[voice]
mode = "toggle"
push_to_talk = "Alt+Super"
```

`Alt+Super` with `toggle` mode is recommended. Press once to start recording, then press again to stop. This avoids hold-mode key conflicts with desktop environments or focused input fields. On Windows, the low-level keyboard hook only observes key edges and never consumes or replays modifiers, so standalone `Alt`, `Super`, and shortcuts such as `Alt+Tab` retain their normal system behavior. Modifier combinations require the exact configured set, and hook fallback state is checked against the unsuppressed physical key state so separate single-key presses cannot be combined into the shortcut.

Voice hotkeys only support keys suitable for global shortcuts:

- Supported: modifier-only combinations, such as `Alt+Super` and `Ctrl+Alt+Shift`.
- Supported: function keys `F1` through `F24`, such as `F9` and `Alt+F8`.
- Supported: non-text control and navigation keys, such as `Tab`, `Enter`, `Escape`, `Backspace`, `CapsLock`, `Up`, `Down`, `Left`, `Right`, `Home`, `End`, `PageUp`, `PageDown`, `Insert`, and `Delete`.
- Not supported: letters, digits, punctuation, Space, numpad digits, and numpad symbols that can enter text, such as `Alt+G`, `G`, `Alt+1`, and `Alt+Space`.

Hotword example:

```toml
[voice]
hotwords = ["Wayland", "Sway", "wl-copy", "wtype", "just-talk-go"]
```

Dropping disfluencies:

The recognizer can strip filler words, hesitation sounds and repeated phrases from its output. Dictation into a machine rarely needs them, but they do carry hesitation, so this is off by default. It is text post-processing and **does not affect duration-based billing**.

```toml
[voice]
smooth_text = true
```

Silence gating (reduces streaming recognition cost):

Streaming ASR is billed by audio duration, so pauses inside a recording cost money too. With silence gating enabled, audio below the level threshold is withheld before upload and only speech reaches the server. Disabled by default.

```toml
[voice.vad]
enabled = true
```

Full settings, with the defaults used for anything omitted:

```toml
[voice.vad]
enabled = false          # turn silence gating on
measure_only = false     # gather statistics only: everything is still uploaded
frame_ms = 20            # analysis frame size; 10-30 is the usual range
adaptive = true          # track the noise floor continuously and derive the threshold (recommended)
noise_factor = 4.0       # threshold = current noise floor x this factor
min_threshold = 0.0015   # floor under the threshold, so noise is never taken for speech
max_threshold = 0.05     # ceiling on the threshold; speech is 0.05-0.3
threshold = 0.004        # fixed threshold, used when adaptive = false
auto_calibrate = false   # legacy: calibrate once from the first 300 ms. Superseded by adaptive
calibrate_ms = 300       # window for auto_calibrate
min_speech_ms = 60       # how long the level must stay high to count as speech; debounces clicks and breaths
pre_roll_ms = 200        # silence re-sent at speech onset, so word beginnings survive
silence_keep_ms = 400    # lead of each pause kept, protecting word tails and punctuation
heartbeat_ms = 3000      # frame interval through long pauses, so the connection is not idle. 0 disables
silence_stop_ms = 0      # trailing window: end the recording when it holds almost no speech. 0 disables; works even with enabled = false
silence_stop_max_speech_pct = 15  # how much of that window may be speech and still count as silence
sweep_thresholds = []    # measure_only: project several candidate thresholds at once
```

Why `adaptive` rather than a fixed threshold: on one real microphone the measured noise floor differed several times over between two consecutive recordings, as hardware noise cancellation or automatic gain engaged, so any fixed threshold is wrong for one of them. Calibrating once at the start of a recording is no better, because users begin speaking the moment they press the hotkey and that window contains nothing but speech. The adaptive floor falls instantly and rises very slowly, so genuine silence corrects it at once while speech cannot lift it.

Reference levels: a quiet room sits near a normalized RMS of 0.001-0.01, speech near 0.05-0.3. If words go missing after enabling it, lower `threshold` or raise `pre_roll_ms`; if background noise is mistaken for speech, raise `threshold` or rely on `auto_calibrate`. When enabling this for the first time, run a few recordings with `measure_only = true`. In that mode all audio is still uploaded, so input cannot break, while the log reports your microphone's real level range to choose `threshold` against.

After each recording the `silence gate summary` log line reports the observed levels and the fraction of frames withheld:

```bash
journalctl --user -u just-talk -n 20 | grep 'silence gate'
```

macOS hotkey example:

```toml
[voice]
# Option is Alt; Command/Cmd is Super.
push_to_talk = "Option+Command"
```

On Windows, `Win` and `Super` both refer to the Windows logo key. If recording is unavailable, allow desktop applications to access the microphone under Windows Settings > Privacy & security > Microphone.


## Changelog

See [CHANGELOG.md](CHANGELOG.md).

## Maintenance And Contributions

Just Talk is maintained by `whoamihappyhacking`.

This project does not accept pull requests. Issues are welcome for bug reports, usage feedback, and feature discussion.

## License

Just Talk is licensed under the GNU General Public License v3.0.

## Project website

Visit the [Just Talk website](https://whoamihappyhacking.github.io/just-talk-go/). GitHub Actions automatically deploys updates to `website/` pushed to `master`.

The static introduction in `website/` includes features, platform support, quick-start instructions, and a simulated demo that does not use the microphone. Preview it with:

```bash
python3 -m http.server 7788 --bind 0.0.0.0 --directory website
```
