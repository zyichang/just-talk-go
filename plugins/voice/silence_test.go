package voice

import (
	"math"
	"testing"

	"github.com/c/just-talk-go/config"
)

const testFrameMs = 20

// frameBytesFor returns the byte length of one frame at the test frame size.
func frameBytesFor(frameMs int) int {
	return pcmSampleRate * pcmBytesPerSample * frameMs / 1000
}

// silentPCM builds frames of digital silence.
func silentPCM(frames int) []byte {
	return make([]byte, frames*frameBytesFor(testFrameMs))
}

// tonePCM builds frames of a 440 Hz sine at the given normalized amplitude,
// standing in for speech.
func tonePCM(frames int, amplitude float64) []byte {
	buf := make([]byte, frames*frameBytesFor(testFrameMs))
	for i := 0; i+1 < len(buf); i += 2 {
		sample := int16(amplitude * pcmFullScale *
			math.Sin(2*math.Pi*440*float64(i/2)/pcmSampleRate))
		buf[i] = byte(uint16(sample) & 0xFF)
		buf[i+1] = byte(uint16(sample) >> 8)
	}
	return buf
}

// testVAD returns a config with calibration off, so tests exercise the gate
// against a known fixed threshold.
func testVAD() config.VADConfig {
	return config.VADConfig{
		Enabled: true, FrameMs: testFrameMs, Threshold: 0.02,
		AutoCalibrate: false, PreRollMs: 0, SilenceKeepMs: 0, HeartbeatMs: 0,
	}
}

func TestFrameRMSSilenceIsZero(t *testing.T) {
	if got := frameRMS(silentPCM(1)); got != 0 {
		t.Fatalf("silence RMS = %v, want 0", got)
	}
}

func TestFrameRMSToneIsNearAmplitude(t *testing.T) {
	// A sine wave's RMS is amplitude/sqrt(2).
	got := frameRMS(tonePCM(1, 0.5))
	want := 0.5 / math.Sqrt2
	if math.Abs(got-want) > 0.02 {
		t.Fatalf("tone RMS = %v, want about %v", got, want)
	}
}

func TestFrameRMSEmptyInput(t *testing.T) {
	if got := frameRMS(nil); got != 0 {
		t.Fatalf("empty RMS = %v, want 0", got)
	}
}

func TestGateDisabledPassesEverythingThrough(t *testing.T) {
	cfg := testVAD()
	cfg.Enabled = false
	gate := newSilenceGate(cfg)

	in := silentPCM(10)
	out := gate.Filter(in)
	if len(out) != len(in) {
		t.Fatalf("disabled gate returned %d bytes, want %d", len(out), len(in))
	}
}

func TestGateDropsSilence(t *testing.T) {
	gate := newSilenceGate(testVAD())

	if out := gate.Filter(silentPCM(50)); len(out) != 0 {
		t.Fatalf("silence produced %d bytes, want 0", len(out))
	}
	s := gate.Summarize()
	if s.TotalFrames != 50 || s.ForwardedFrames != 0 || s.WithheldRatio != 1 {
		t.Fatalf("summary = %+v, want 50 frames, 0 forwarded, ratio 1", s)
	}
}

func TestGateKeepsSpeech(t *testing.T) {
	gate := newSilenceGate(testVAD())

	in := tonePCM(50, 0.3)
	out := gate.Filter(in)
	if len(out) != len(in) {
		t.Fatalf("speech produced %d bytes, want %d", len(out), len(in))
	}
	if s := gate.Summarize(); s.WithheldRatio != 0 {
		t.Fatalf("withheld ratio = %v, want 0 for pure speech", s.WithheldRatio)
	}
}

func TestGatePreRollRecoversWordOnset(t *testing.T) {
	cfg := testVAD()
	cfg.PreRollMs = 40 // two frames
	gate := newSilenceGate(cfg)

	// Ten silent frames are dropped, but the last two must be retained.
	if out := gate.Filter(silentPCM(10)); len(out) != 0 {
		t.Fatalf("silence produced %d bytes, want 0", len(out))
	}
	out := gate.Filter(tonePCM(1, 0.3))
	wantFrames := 3 // two pre-roll frames plus the speech frame
	if got := len(out) / frameBytesFor(testFrameMs); got != wantFrames {
		t.Fatalf("speech onset produced %d frames, want %d", got, wantFrames)
	}
}

func TestGateKeepsLeadOfEachPause(t *testing.T) {
	cfg := testVAD()
	cfg.SilenceKeepMs = 60 // three frames
	gate := newSilenceGate(cfg)

	gate.Filter(tonePCM(1, 0.3)) // establish speaking state
	out := gate.Filter(silentPCM(10))
	wantFrames := 3
	if got := len(out) / frameBytesFor(testFrameMs); got != wantFrames {
		t.Fatalf("pause produced %d frames, want %d", got, wantFrames)
	}
}

func TestGateHeartbeatKeepsConnectionFed(t *testing.T) {
	cfg := testVAD()
	cfg.HeartbeatMs = 100 // every five frames
	gate := newSilenceGate(cfg)

	// Twenty silent frames: frames 5, 10, 15 and 20 are heartbeats.
	out := gate.Filter(silentPCM(20))
	if got := len(out) / frameBytesFor(testFrameMs); got != 4 {
		t.Fatalf("heartbeat produced %d frames, want 4", got)
	}
}

func TestGateAutoCalibrateRaisesThresholdAboveNoise(t *testing.T) {
	cfg := testVAD()
	cfg.AutoCalibrate = true
	cfg.CalibrateMs = 100 // five frames
	cfg.NoiseFactor = 3.0
	gate := newSilenceGate(cfg)

	// Background hiss louder than the default threshold but far below speech.
	noise := tonePCM(5, 0.03)
	if out := gate.Filter(noise); len(out) != len(noise) {
		t.Fatalf("calibration window withheld audio; got %d bytes, want %d", len(out), len(noise))
	}
	if !gate.calibrated {
		t.Fatal("gate did not finish calibrating")
	}
	if gate.threshold <= 0.02 {
		t.Fatalf("threshold = %v, want it raised above the 0.02 default", gate.threshold)
	}
	// That same hiss must now be treated as silence.
	if out := gate.Filter(tonePCM(5, 0.03)); len(out) != 0 {
		t.Fatalf("post-calibration hiss produced %d bytes, want 0", len(out))
	}
}

// Regression test. Calibration used to average the window and multiply by
// NoiseFactor. A user who began speaking immediately turned that average into a
// speech measurement, producing a threshold above all real speech, and every
// later frame was discarded as silence: the recording uploaded nothing and no
// text came back. Speech during calibration must never silence the gate.
func TestGateCalibrationDuringSpeechStillPassesSpeech(t *testing.T) {
	cfg := testVAD()
	cfg.AutoCalibrate = true
	cfg.CalibrateMs = 300 // fifteen frames, the shipped default
	cfg.NoiseFactor = 3.0
	gate := newSilenceGate(cfg)

	// The user is already talking when recording starts.
	loud := tonePCM(15, 0.3)
	if out := gate.Filter(loud); len(out) != len(loud) {
		t.Fatalf("calibration window withheld audio; got %d bytes, want %d", len(out), len(loud))
	}

	// Speech at the same level must still get through afterwards.
	more := tonePCM(20, 0.3)
	out := gate.Filter(more)
	if len(out) != len(more) {
		t.Fatalf("post-calibration speech produced %d bytes, want %d "+
			"(threshold=%v) — the gate swallowed real speech",
			len(out), len(more), gate.threshold)
	}
	if s := gate.Summarize(); s.SpeechFrames != 20 {
		t.Fatalf("speech frames = %d, want 20", s.SpeechFrames)
	}
}

func TestGateCalibrationRespectsCeiling(t *testing.T) {
	cfg := testVAD()
	cfg.AutoCalibrate = true
	cfg.CalibrateMs = 100
	cfg.NoiseFactor = 100 // absurd, to prove the ceiling binds
	cfg.MaxThreshold = 0.05
	gate := newSilenceGate(cfg)

	gate.Filter(tonePCM(5, 0.03))
	if gate.threshold > 0.05 {
		t.Fatalf("threshold = %v, want it capped at 0.05", gate.threshold)
	}
}

func TestGateCalibrationUsesQuietestFrame(t *testing.T) {
	cfg := testVAD()
	cfg.AutoCalibrate = true
	cfg.CalibrateMs = 60 // three frames
	cfg.NoiseFactor = 3.0
	gate := newSilenceGate(cfg)

	// Two loud frames and one quiet frame: the quiet one is the noise floor.
	gate.Filter(tonePCM(2, 0.3))
	gate.Filter(tonePCM(1, 0.002))
	if !gate.calibrated {
		t.Fatal("gate did not finish calibrating")
	}
	// 0.002 amplitude is an RMS near 0.0014; times three stays under the 0.02
	// configured floor, so the floor must win rather than the loud frames.
	if gate.threshold != 0.02 {
		t.Fatalf("threshold = %v, want the 0.02 configured floor", gate.threshold)
	}
}

func TestGateSummaryReportsLevels(t *testing.T) {
	gate := newSilenceGate(testVAD())

	gate.Filter(silentPCM(5))
	gate.Filter(tonePCM(5, 0.4))

	s := gate.Summarize()
	if s.MinLevel != 0 {
		t.Errorf("min level = %v, want 0 from the silent frames", s.MinLevel)
	}
	want := 0.4 / math.Sqrt2
	if math.Abs(s.MaxLevel-want) > 0.02 {
		t.Errorf("max level = %v, want about %v", s.MaxLevel, want)
	}
	if s.MeanLevel <= 0 || s.MeanLevel >= s.MaxLevel {
		t.Errorf("mean level = %v, want between 0 and %v", s.MeanLevel, s.MaxLevel)
	}
}

// The percentiles are the tuning instrument: on a half-silent, half-loud
// recording the low percentiles must land on the silence cluster and the high
// ones on the speech cluster, which is what makes the valley between them
// visible.
func TestGateSummaryReportsPercentiles(t *testing.T) {
	gate := newSilenceGate(testVAD())

	gate.Filter(silentPCM(50))
	gate.Filter(tonePCM(50, 0.4))

	s := gate.Summarize()
	loud := 0.4 / math.Sqrt2
	if s.P10 != 0 || s.P25 != 0 {
		t.Errorf("p10=%v p25=%v, want 0 from the silent half", s.P10, s.P25)
	}
	if math.Abs(s.P75-loud) > 0.02 || math.Abs(s.P90-loud) > 0.02 {
		t.Errorf("p75=%v p90=%v, want about %v from the loud half", s.P75, s.P90, loud)
	}
	if !(s.P25 <= s.P50 && s.P50 <= s.P75) {
		t.Errorf("percentiles not monotonic: p25=%v p50=%v p75=%v", s.P25, s.P50, s.P75)
	}
}

func TestPercentileEdgeCases(t *testing.T) {
	if got := percentile(nil, 0.5); got != 0 {
		t.Errorf("percentile(nil) = %v, want 0", got)
	}
	one := []float64{7}
	if got := percentile(one, 1.0); got != 7 {
		t.Errorf("percentile at 1.0 = %v, want 7 (must not index out of range)", got)
	}
}

func TestGateHandlesPartialFrames(t *testing.T) {
	gate := newSilenceGate(testVAD())
	frame := frameBytesFor(testFrameMs)

	// Feed one and a half frames of speech; only the complete frame may emerge.
	in := tonePCM(2, 0.3)
	out := gate.Filter(in[:frame+frame/2])
	if got := len(out); got != frame {
		t.Fatalf("partial feed produced %d bytes, want %d", got, frame)
	}
	// The held remainder completes on the next call.
	out = gate.Filter(in[frame+frame/2:])
	if got := len(out); got != frame {
		t.Fatalf("completing feed produced %d bytes, want %d", got, frame)
	}
}

func TestGateSavingsOnMixedAudio(t *testing.T) {
	gate := newSilenceGate(testVAD())

	gate.Filter(tonePCM(10, 0.3)) // 10 speech frames
	gate.Filter(silentPCM(30))    // 30 silent frames

	s := gate.Summarize()
	if s.TotalFrames != 40 {
		t.Fatalf("total frames = %d, want 40", s.TotalFrames)
	}
	if s.ForwardedFrames != 10 {
		t.Fatalf("forwarded frames = %d, want 10", s.ForwardedFrames)
	}
	if math.Abs(s.WithheldRatio-0.75) > 1e-9 {
		t.Fatalf("withheld ratio = %v, want 0.75", s.WithheldRatio)
	}
}

func TestMsToFramesRoundsUp(t *testing.T) {
	cases := []struct{ ms, frameMs, want int }{
		{0, 20, 0},
		{20, 20, 1},
		{21, 20, 2},
		{400, 20, 20},
		{100, 0, 0},
	}
	for _, c := range cases {
		if got := msToFrames(c.ms, c.frameMs); got != c.want {
			t.Errorf("msToFrames(%d, %d) = %d, want %d", c.ms, c.frameMs, got, c.want)
		}
	}
}

func TestGateMeasureOnlyUploadsEverythingButStillCounts(t *testing.T) {
	cfg := testVAD()
	cfg.MeasureOnly = true
	gate := newSilenceGate(cfg)

	// Silence would normally be dropped entirely.
	in := silentPCM(30)
	out := gate.Filter(in)
	if len(out) != len(in) {
		t.Fatalf("measure-only withheld audio: got %d bytes, want %d", len(out), len(in))
	}
	// The summary must still report what would have been saved.
	s := gate.Summarize()
	if s.TotalFrames != 30 {
		t.Fatalf("total frames = %d, want 30", s.TotalFrames)
	}
	if s.WithheldRatio != 1 {
		t.Fatalf("withheld ratio = %v, want 1 as the potential saving", s.WithheldRatio)
	}
}

// The sweep is the tuning instrument: one recording must yield the whole
// threshold trade-off curve, and the shadow gates must never alter the upload.
func TestGateSweepProjectsAlternativeThresholds(t *testing.T) {
	cfg := testVAD()
	cfg.MeasureOnly = true
	cfg.SweepThresholds = []float64{0.002, 0.05, 0}
	gate := newSilenceGate(cfg)

	// Quiet speech: above 0.002 but below 0.05.
	in := tonePCM(40, 0.01)
	out := gate.Filter(in)
	if len(out) != len(in) {
		t.Fatalf("measure-only sweep altered the upload: got %d bytes, want %d", len(out), len(in))
	}

	projections := gate.Projections()
	if len(projections) != 2 {
		t.Fatalf("got %d projections, want 2 (the zero threshold must be skipped)", len(projections))
	}
	low, high := projections[0], projections[1]
	if low.Threshold != 0.002 || high.Threshold != 0.05 {
		t.Fatalf("thresholds = %v, %v; want 0.002, 0.05", low.Threshold, high.Threshold)
	}
	// The low threshold sees this as speech, so it withholds nothing.
	if low.WithheldRatio != 0 {
		t.Errorf("threshold 0.002 would withhold %v, want 0", low.WithheldRatio)
	}
	// The high threshold sees the same audio as silence and would discard it.
	if high.WithheldRatio == 0 {
		t.Errorf("threshold 0.05 would withhold nothing; want it to reject this audio")
	}
	if high.SpeechFrames != 0 {
		t.Errorf("threshold 0.05 found %d speech frames, want 0", high.SpeechFrames)
	}
}

// speech_runs is what exposes a threshold set too high: it chops continuous
// speech into many fragments, each paying pre-roll and silence-lead overhead.
func TestGateCountsSpeechRuns(t *testing.T) {
	gate := newSilenceGate(testVAD())

	gate.Filter(tonePCM(5, 0.3)) // run one
	gate.Filter(silentPCM(30))   // a real pause
	gate.Filter(tonePCM(5, 0.3)) // run two
	gate.Filter(silentPCM(30))   // another pause
	gate.Filter(tonePCM(5, 0.3)) // run three

	if s := gate.Summarize(); s.SpeechRuns != 3 {
		t.Fatalf("speech runs = %d, want 3", s.SpeechRuns)
	}
}

func TestGateSweepIgnoredWhenNotMeasuring(t *testing.T) {
	cfg := testVAD()
	cfg.MeasureOnly = false
	cfg.SweepThresholds = []float64{0.002, 0.05}
	gate := newSilenceGate(cfg)

	if len(gate.Projections()) != 0 {
		t.Fatal("sweep must only run in measure-only mode")
	}
}

// The adaptive floor is what makes the gate survive a microphone whose noise
// level moves between sessions. Measured floors on one real microphone differed
// by four times over two consecutive recordings, so no fixed threshold worked
// for both; the trailing minimum tracks whichever floor is current.
func TestGateAdaptiveTracksMovingNoiseFloor(t *testing.T) {
	cfg := testVAD()
	cfg.Adaptive = true
	cfg.AdaptiveWindowMs = 200 // ten frames
	cfg.NoiseFactor = 4.0
	cfg.MinThreshold = 0.0015
	gate := newSilenceGate(cfg)

	// A quiet room. The floor starts at zero and climbs slowly, so the
	// threshold stays at MinThreshold: permissive, never rejecting speech.
	gate.Filter(tonePCM(10, 0.0007))
	quietThreshold := gate.threshold
	if quietThreshold != 0.0015 {
		t.Fatalf("quiet-room threshold = %v, want the 0.0015 floor", quietThreshold)
	}

	// Sustained louder noise: the floor must climb, but only over many frames.
	gate.Filter(tonePCM(3000, 0.008))
	noisyThreshold := gate.threshold
	if noisyThreshold <= quietThreshold {
		t.Fatalf("threshold did not rise with the noise floor: %v then %v",
			quietThreshold, noisyThreshold)
	}
	if gate.floorCurrent <= 0 {
		t.Fatal("noise floor was never established")
	}
}

func TestGateAdaptiveRespectsBothBounds(t *testing.T) {
	cfg := testVAD()
	cfg.Adaptive = true
	cfg.AdaptiveWindowMs = 100
	cfg.NoiseFactor = 1000 // absurd, to drive the ceiling
	cfg.MinThreshold = 0.002
	cfg.MaxThreshold = 0.05
	gate := newSilenceGate(cfg)

	gate.Filter(tonePCM(5, 0.01))
	if gate.threshold > 0.05 {
		t.Errorf("threshold = %v, want it capped at 0.05", gate.threshold)
	}

	// Digital silence would drive the derived threshold to zero.
	quiet := newSilenceGate(cfg)
	quiet.Filter(silentPCM(5))
	if quiet.threshold < 0.002 {
		t.Errorf("threshold = %v, want it floored at 0.002", quiet.threshold)
	}
}

// A long pause must be trimmed while short inter-word gaps are left alone,
// because the pre-roll and silence-lead overhead exceeds a short gap entirely.
func TestGateAdaptiveTrimsLongPauseNotShortGaps(t *testing.T) {
	cfg := testVAD()
	cfg.Adaptive = true
	cfg.AdaptiveWindowMs = 1000
	cfg.NoiseFactor = 4.0
	cfg.MinThreshold = 0.0015
	cfg.PreRollMs = 200
	cfg.SilenceKeepMs = 400
	gate := newSilenceGate(cfg)

	// Speech, a 5-second pause, then speech again.
	gate.Filter(tonePCM(50, 0.05))
	gate.Filter(silentPCM(250))
	gate.Filter(tonePCM(50, 0.05))

	s := gate.Summarize()
	// The pause is 250 frames; keeping 400 ms of it leaves about 230 dropped.
	if s.WithheldRatio < 0.5 {
		t.Fatalf("withheld ratio = %v, want over 0.5 from a five-second pause "+
			"(threshold=%v floor=%v)", s.WithheldRatio, s.Threshold, s.NoiseFloor)
	}
	if s.SpeechRuns != 2 {
		t.Errorf("speech runs = %d, want 2", s.SpeechRuns)
	}
}

// Debouncing is what stopped transients from shredding a long pause into many
// short speech runs, each paying pre-roll and silence-lead overhead.
func TestGateDebounceIgnoresTransientBlips(t *testing.T) {
	cfg := testVAD()
	cfg.MinSpeechMs = 60 // three frames
	cfg.SilenceKeepMs = 100
	gate := newSilenceGate(cfg)

	// A long pause interrupted by single-frame clicks, as a keyboard or a breath
	// would produce.
	for i := 0; i < 10; i++ {
		gate.Filter(silentPCM(20))
		gate.Filter(tonePCM(1, 0.3)) // one loud frame only
	}

	s := gate.Summarize()
	if s.SpeechRuns != 0 {
		t.Fatalf("speech runs = %d, want 0: single frames must not open a run", s.SpeechRuns)
	}
	// Without debouncing each blip reopened the keep window and almost nothing
	// was saved. The pause must now be withheld.
	if s.WithheldRatio < 0.8 {
		t.Fatalf("withheld ratio = %v, want over 0.8 across a click-interrupted pause",
			s.WithheldRatio)
	}
}

func TestGateDebounceStillAcceptsRealSpeech(t *testing.T) {
	cfg := testVAD()
	cfg.MinSpeechMs = 60 // three frames
	gate := newSilenceGate(cfg)

	out := gate.Filter(tonePCM(20, 0.3))
	// All twenty frames must reach the server: the two candidate frames that
	// preceded confirmation are recovered from the pre-roll buffer.
	if got := len(out) / frameBytesFor(testFrameMs); got != 20 {
		t.Fatalf("forwarded %d frames of 20 — the debounce lost the start of speech", got)
	}
	if s := gate.Summarize(); s.SpeechRuns != 1 {
		t.Fatalf("speech runs = %d, want 1", s.SpeechRuns)
	}
}

func TestGateDebounceNeverStarvesPreRoll(t *testing.T) {
	cfg := testVAD()
	cfg.MinSpeechMs = 200 // ten frames
	cfg.PreRollMs = 20    // one frame, deliberately too small
	gate := newSilenceGate(cfg)

	if gate.preRollFrames < gate.minSpeechFrames {
		t.Fatalf("pre-roll %d frames is smaller than the %d-frame debounce window",
			gate.preRollFrames, gate.minSpeechFrames)
	}
	// Confirmed speech must still arrive whole.
	out := gate.Filter(tonePCM(15, 0.3))
	if got := len(out) / frameBytesFor(testFrameMs); got != 15 {
		t.Fatalf("forwarded %d frames of 15", got)
	}
}

func BenchmarkFrameRMS20ms(b *testing.B) {
	frame := tonePCM(1, 0.3)
	b.SetBytes(int64(len(frame)))
	for i := 0; i < b.N; i++ {
		_ = frameRMS(frame)
	}
}

func BenchmarkGateFilter200ms(b *testing.B) {
	gate := newSilenceGate(testVAD())
	chunk := tonePCM(10, 0.3) // 200 ms, the size streamAudio reads
	b.SetBytes(int64(len(chunk)))
	for i := 0; i < b.N; i++ {
		_ = gate.Filter(chunk)
	}
}
