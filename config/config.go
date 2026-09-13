package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/c/just-talk-go/hotkey"
)

type Config struct {
	Voice   VoiceConfig   `toml:"voice"`
	Debug   DebugConfig   `toml:"debug"`
	Overlay OverlayConfig `toml:"overlay"`
}

type DebugConfig struct {
	Enabled bool     `toml:"enabled"`
	Hotkeys []string `toml:"hotkeys"`
}

type OverlayConfig struct {
	Enabled     bool    `toml:"enabled"`
	Position    string  `toml:"position"`
	IdleVisible bool    `toml:"idle_visible"`
	Scale       float64 `toml:"scale"`
}

type VoiceConfig struct {
	Enabled     bool      `toml:"enabled"`
	Mode        string    `toml:"mode"`
	PushToTalk  string    `toml:"push_to_talk"`
	Device      string    `toml:"device"`
	Gain        int       `toml:"gain"`
	StopDelayMs int       `toml:"stop_delay_ms"`
	Language    string    `toml:"language"`
	AutoSubmit  bool      `toml:"auto_submit"`
	AppKey      string    `toml:"app_key"`
	AccessKey   string    `toml:"access_key"`
	ResourceID  string    `toml:"resource_id"`
	Hotwords    []string  `toml:"hotwords"`
	VAD         VADConfig `toml:"vad"`
}

// VADConfig controls client-side silence gating. Streaming ASR is billed by
// audio duration, so dropping near-silent audio before upload reduces cost.
// Disabled by default: enabling it changes what the recognizer receives.
type VADConfig struct {
	// Enabled turns silence gating on. When false the audio stream is untouched.
	Enabled bool `toml:"enabled"`
	// MeasureOnly runs the gate for its statistics but uploads everything
	// anyway. Use it to learn a microphone's real levels, and what the savings
	// would be, without any risk of discarding speech.
	MeasureOnly bool `toml:"measure_only"`
	// FrameMs is the analysis frame size. 10-30 is the usual range; larger
	// frames make the gate coarser and waste audio around speech edges.
	FrameMs int `toml:"frame_ms"`
	// Threshold is the normalized RMS level (0..1) at or above which a frame
	// counts as speech. A quiet room sits near 0.001-0.01; speech near 0.05-0.3.
	Threshold float64 `toml:"threshold"`
	// AutoCalibrate raises Threshold to match the room's measured noise floor
	// during the first CalibrateMs of each recording.
	//
	// Off by default, and superseded by Adaptive. A one-shot window almost
	// always contains speech, because users start talking as soon as they press
	// the hotkey, so the derived threshold varies wildly between sessions and
	// can land high enough to reject every later frame.
	AutoCalibrate bool `toml:"auto_calibrate"`
	// Adaptive tracks the noise floor continuously as the quietest frame in a
	// trailing window, and derives the threshold from it on every frame.
	//
	// This is the only scheme that survives real recordings. A fixed threshold
	// breaks when the microphone's noise floor moves between sessions, which it
	// does by several times over when hardware noise cancellation or automatic
	// gain engages. One-shot calibration breaks when the user is already
	// talking. A trailing minimum needs neither assumption: speech always
	// leaves quiet gaps between words, so the recent minimum tracks the floor
	// whether or not anyone is speaking.
	Adaptive bool `toml:"adaptive"`
	// AdaptiveWindowMs is the trailing window the noise floor is measured over.
	// It must be long enough to always contain a gap between words.
	AdaptiveWindowMs int `toml:"adaptive_window_ms"`
	// MinThreshold floors the adaptive threshold, so an unnaturally quiet
	// stretch cannot drive it low enough to treat noise as speech.
	MinThreshold float64 `toml:"min_threshold"`
	// CalibrateMs is the calibration window. Audio in it is always uploaded,
	// because the user may already be speaking.
	CalibrateMs int `toml:"calibrate_ms"`
	// NoiseFactor multiplies the measured noise floor to derive the threshold.
	NoiseFactor float64 `toml:"noise_factor"`
	// MaxThreshold caps whatever calibration derives. Speech sits near
	// 0.05-0.3, so a noise-derived threshold above this ceiling can only be a
	// mis-measurement, and letting it stand would discard the whole recording.
	MaxThreshold float64 `toml:"max_threshold"`
	// MinSpeechMs is how long the level must stay above the threshold before a
	// frame run counts as speech. It debounces transients: a keyboard click or
	// a breath crosses the threshold for one frame, and without this each such
	// blip opened a new speech run that paid PreRollMs plus SilenceKeepMs of
	// overhead. On an 80-second recording those blips produced 120 runs and ate
	// roughly half the savings. Real speech sustains far longer than one frame.
	MinSpeechMs int `toml:"min_speech_ms"`
	// PreRollMs of dropped audio is retained and re-sent when speech starts, so
	// the quiet onset of a word is not clipped.
	PreRollMs int `toml:"pre_roll_ms"`
	// SilenceKeepMs of each pause is still uploaded, preserving the word tail
	// and giving the recognizer the pause it needs to punctuate.
	SilenceKeepMs int `toml:"silence_keep_ms"`
	// HeartbeatMs forwards one frame at this interval through a long pause, so
	// the server does not treat the connection as idle. 0 disables it.
	HeartbeatMs int `toml:"heartbeat_ms"`
	// SweepThresholds projects, in MeasureOnly mode, what each of these
	// thresholds would have withheld. One recording then yields the whole
	// trade-off curve instead of one point per recording. Ignored otherwise.
	SweepThresholds []float64 `toml:"sweep_thresholds"`
}

func Default() *Config {
	return &Config{
		Voice: VoiceConfig{
			Enabled: true, Mode: "toggle", PushToTalk: "Alt+Super",
			Language: "zh-CN", AutoSubmit: true, ResourceID: "volc.bigasr.sauc.duration",
			VAD: VADConfig{
				Enabled: false, FrameMs: 20,
				Adaptive: true, AdaptiveWindowMs: 4000, NoiseFactor: 4.0,
				MinThreshold: 0.0015, MaxThreshold: 0.05, Threshold: 0.004,
				AutoCalibrate: false, CalibrateMs: 300,
				MinSpeechMs: 60, PreRollMs: 200, SilenceKeepMs: 400, HeartbeatMs: 3000,
			},
		},
		Overlay: OverlayConfig{
			Enabled: true, Position: "bottom-center", IdleVisible: false, Scale: 1.0,
		},
	}
}

func Load(path string) (*Config, error) {
	cfg := Default()
	if path == "" {
		path = FindConfig()
	}
	if path == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	return cfg, nil
}

func FindConfig() string {
	candidates := []string{"./config.toml"}
	if path := DefaultPath(); path != "" {
		candidates = append(candidates, path)
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		candidates = append(candidates, filepath.Join(xdg, "just-talk", "config.toml"))
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func Save(cfg *Config) error {
	path := FindConfig()
	if path == "" {
		path = DefaultPath()
		if path == "" {
			return fmt.Errorf("cannot determine user config directory")
		}
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(cfg)
}

// DefaultPath returns the platform-standard per-user configuration path.
func DefaultPath() string {
	if runtime.GOOS == "windows" {
		if dir, err := os.UserConfigDir(); err == nil && strings.TrimSpace(dir) != "" {
			return filepath.Join(dir, "just-talk", "config.toml")
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config", "just-talk", "config.toml")
	}
	return ""
}

// ---- Hotkey parser ----

var modifierNames = map[string]hotkey.Modifier{
	"ctrl": hotkey.ModCtrl, "alt": hotkey.ModAlt, "shift": hotkey.ModShift,
	"control": hotkey.ModCtrl, "option": hotkey.ModAlt, "super": hotkey.ModSuper,
	"cmd": hotkey.ModSuper, "command": hotkey.ModSuper, "win": hotkey.ModSuper,
}

var keyNameToCode = buildKeyNameMap()

func buildKeyNameMap() map[string]hotkey.KeyCode {
	m := make(map[string]hotkey.KeyCode)
	for i := hotkey.KeyA; i <= hotkey.KeyZ; i++ {
		m[strings.ToLower(i.String())] = i
		m[i.String()] = i
	}
	for i := hotkey.Key0; i <= hotkey.Key9; i++ {
		m[i.String()] = i
	}
	for i := hotkey.KeyF1; i <= hotkey.KeyF24; i++ {
		m[strings.ToLower(i.String())] = i
		m[i.String()] = i
	}
	m["ctrl"] = hotkey.KeyCtrl
	m["control"] = hotkey.KeyCtrl
	m["alt"] = hotkey.KeyAlt
	m["option"] = hotkey.KeyAlt
	m["shift"] = hotkey.KeyShift
	m["super"] = hotkey.KeySuper
	m["cmd"] = hotkey.KeySuper
	m["command"] = hotkey.KeySuper
	m["win"] = hotkey.KeySuper
	for _, k := range []hotkey.KeyCode{
		hotkey.KeySpace, hotkey.KeyTab, hotkey.KeyEnter, hotkey.KeyEscape,
		hotkey.KeyBackspace, hotkey.KeyCapsLock,
		hotkey.KeyArrowUp, hotkey.KeyArrowDown, hotkey.KeyArrowLeft, hotkey.KeyArrowRight,
		hotkey.KeyHome, hotkey.KeyEnd, hotkey.KeyPageUp, hotkey.KeyPageDown,
		hotkey.KeyInsert, hotkey.KeyDelete,
		hotkey.KeyNum0, hotkey.KeyNum1, hotkey.KeyNum2, hotkey.KeyNum3, hotkey.KeyNum4,
		hotkey.KeyNum5, hotkey.KeyNum6, hotkey.KeyNum7, hotkey.KeyNum8, hotkey.KeyNum9,
		hotkey.KeyBacktick, hotkey.KeyMinus, hotkey.KeyEqual,
		hotkey.KeyLeftBracket, hotkey.KeyRightBracket, hotkey.KeyBackslash,
		hotkey.KeySemicolon, hotkey.KeyQuote,
		hotkey.KeyComma, hotkey.KeyPeriod, hotkey.KeySlash,
	} {
		m[strings.ToLower(k.String())] = k
		m[k.String()] = k
	}
	m["space"] = hotkey.KeySpace
	m["enter"] = hotkey.KeyEnter
	m["return"] = hotkey.KeyEnter
	m["esc"] = hotkey.KeyEscape
	m["escape"] = hotkey.KeyEscape
	m["backspace"] = hotkey.KeyBackspace
	m["tab"] = hotkey.KeyTab
	m["up"] = hotkey.KeyArrowUp
	m["down"] = hotkey.KeyArrowDown
	m["left"] = hotkey.KeyArrowLeft
	m["right"] = hotkey.KeyArrowRight
	m["home"] = hotkey.KeyHome
	m["end"] = hotkey.KeyEnd
	m["pageup"] = hotkey.KeyPageUp
	m["pagedown"] = hotkey.KeyPageDown
	m["insert"] = hotkey.KeyInsert
	m["delete"] = hotkey.KeyDelete
	m["capslock"] = hotkey.KeyCapsLock
	m["`"] = hotkey.KeyBacktick
	m["-"] = hotkey.KeyMinus
	m["="] = hotkey.KeyEqual
	m["["] = hotkey.KeyLeftBracket
	m["]"] = hotkey.KeyRightBracket
	m["\\"] = hotkey.KeyBackslash
	m[";"] = hotkey.KeySemicolon
	m["'"] = hotkey.KeyQuote
	m[","] = hotkey.KeyComma
	m["."] = hotkey.KeyPeriod
	m["/"] = hotkey.KeySlash
	return m
}

func ParseHotkey(s string) (hotkey.Combo, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return hotkey.Combo{}, fmt.Errorf("empty hotkey string")
	}
	parts := strings.Split(s, "+")
	var mods hotkey.Modifier
	var key hotkey.KeyCode
	for _, part := range parts {
		part = strings.TrimSpace(part)
		lower := strings.ToLower(part)
		if mod, ok := modifierNames[lower]; ok {
			mods |= mod
			continue
		}
		if k, ok := keyNameToCode[lower]; ok {
			if key != hotkey.KeyNone {
				return hotkey.Combo{}, fmt.Errorf("multiple keys in %q", s)
			}
			if k.IsModifier() && key == hotkey.KeyNone {
				key = k
			} else if !k.IsModifier() {
				key = k
			}
			continue
		}
		return hotkey.Combo{}, fmt.Errorf("unknown key %q in %q", part, s)
	}
	if key == hotkey.KeyNone && mods != hotkey.ModNone {
		return hotkey.Combo{Mods: mods, Key: hotkey.KeyNone}, nil
	}
	if key != hotkey.KeyNone && mods == hotkey.ModNone && !key.IsModifier() {
		return hotkey.Combo{Mods: hotkey.ModNone, Key: key}, nil
	}
	if key.IsModifier() && mods == hotkey.ModNone {
		return hotkey.Combo{Mods: hotkey.KeyCodeToModifier(key), Key: hotkey.KeyNone}, nil
	}
	if key != hotkey.KeyNone && mods != hotkey.ModNone {
		return hotkey.Combo{Mods: mods, Key: key}, nil
	}
	return hotkey.Combo{}, fmt.Errorf("cannot parse hotkey %q", s)
}

func ParseHotkeys(strings []string) ([]hotkey.Combo, error) {
	var combos []hotkey.Combo
	for _, s := range strings {
		c, err := ParseHotkey(s)
		if err != nil {
			return nil, err
		}
		combos = append(combos, c)
	}
	return combos, nil
}
