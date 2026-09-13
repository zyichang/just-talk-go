package voice

import (
	"math"
	"sort"

	"github.com/c/just-talk-go/config"
)

// Client-side silence gating. Streaming ASR is billed by audio duration, so a
// frame withheld here is a frame never paid for. The gate sits between the
// recorder and the ASR client and decides, per fixed-size frame, whether that
// frame is worth uploading.
//
// The decision is made on the frame in hand, before it is sent. Nothing is
// edited retroactively and no future audio is required.
//
//	silent   --speech--> speaking   (pre-roll frames are flushed first)
//	speaking --silence-> silent     (a short lead of the pause is still sent)
//
// Two details keep recognition quality intact:
//
// Pre-roll exists because the onset of a syllable is quiet as it ramps up.
// Gating strictly on level would clip word beginnings and mis-transcribe them,
// so recent dropped frames are retained and re-sent the moment speech starts.
//
// Silence lead exists because the server places punctuation and splits
// utterances using pauses. Dropping a pause entirely produces run-on text, so
// the beginning of every silent stretch is forwarded as a boundary marker. That
// lead also covers the tail of the preceding word.
//
// A heartbeat forwards one frame periodically through long pauses so an idle
// WebSocket is not closed by the server.

const (
	pcmBytesPerSample = 2
	pcmSampleRate     = 16000
	pcmFullScale      = 32768.0
)

// floorRiseRate is how fast the noise floor may climb per frame, as a fraction
// of the gap to the current level. Internal tuning constant, not deployment
// configuration: it must be slow enough that a burst of speech cannot lift the
// floor into speech territory, and fast enough to follow a genuine change in
// room noise within tens of seconds. At 20 ms frames, 0.001 is a time constant
// of roughly twenty seconds.
const floorRiseRate = 0.001

// frameRMS returns the root-mean-square amplitude of s16le PCM, normalized to
// 0..1. Samples are squared before averaging because a waveform swings
// symmetrically about zero; a plain mean would cancel to nearly nothing and
// could not distinguish speech from silence.
func frameRMS(pcm []byte) float64 {
	var sum float64
	n := 0
	for i := 0; i+1 < len(pcm); i += pcmBytesPerSample {
		s := float64(int16(uint16(pcm[i]) | uint16(pcm[i+1])<<8))
		sum += s * s
		n++
	}
	if n == 0 {
		return 0
	}
	return math.Sqrt(sum/float64(n)) / pcmFullScale
}

// silenceGate filters near-silent frames out of a live PCM stream.
type silenceGate struct {
	enabled     bool
	measureOnly bool
	frameBytes  int
	threshold   float64

	preRollFrames   int
	keepFrames      int
	heartbeatEvery  int
	minSpeechFrames int

	calibrateFrames int
	noiseFactor     float64
	maxThreshold    float64
	minThreshold    float64
	calibrated      bool
	calFrames       int
	calMin          float64

	adaptive     bool
	floorCurrent float64

	pending   []byte
	preBuf    [][]byte
	speaking  bool
	silentRun int
	aboveRun  int

	totalFrames     int
	forwardedFrames int
	speechFrames    int
	speechRuns      int
	minLevel        float64
	maxLevel        float64
	sumLevel        float64
	levels          []float64
	shadows         []*silenceGate
}

// msToFrames converts a duration in milliseconds to whole frames, rounding up
// so a configured duration is never silently truncated to zero.
func msToFrames(ms, frameMs int) int {
	if ms <= 0 || frameMs <= 0 {
		return 0
	}
	return (ms + frameMs - 1) / frameMs
}

func newSilenceGate(cfg config.VADConfig) *silenceGate {
	frameMs := cfg.FrameMs
	if frameMs <= 0 {
		frameMs = 20
	}
	threshold := cfg.Threshold
	if threshold <= 0 {
		threshold = 0.02
	}
	noiseFactor := cfg.NoiseFactor
	if noiseFactor <= 0 {
		noiseFactor = 3.0
	}
	maxThreshold := cfg.MaxThreshold
	if maxThreshold <= 0 {
		maxThreshold = 0.05
	}
	minThreshold := cfg.MinThreshold
	if minThreshold <= 0 {
		minThreshold = 0.0015
	}
	g := &silenceGate{
		enabled:        cfg.Enabled,
		measureOnly:    cfg.MeasureOnly,
		frameBytes:     pcmSampleRate * pcmBytesPerSample * frameMs / 1000,
		threshold:      threshold,
		preRollFrames:  msToFrames(cfg.PreRollMs, frameMs),
		keepFrames:     msToFrames(cfg.SilenceKeepMs, frameMs),
		heartbeatEvery: msToFrames(cfg.HeartbeatMs, frameMs),
		noiseFactor:    noiseFactor,
		maxThreshold:   maxThreshold,
		minThreshold:   minThreshold,
		adaptive:       cfg.Adaptive,
	}
	g.minSpeechFrames = msToFrames(cfg.MinSpeechMs, frameMs)
	if g.minSpeechFrames < 1 {
		g.minSpeechFrames = 1
	}
	// Candidate frames wait in the pre-roll buffer until speech is confirmed, so
	// the buffer must be able to hold a whole debounce window or the beginning
	// of every utterance would be dropped.
	if g.preRollFrames < g.minSpeechFrames {
		g.preRollFrames = g.minSpeechFrames
	}
	if cfg.AutoCalibrate {
		g.calibrateFrames = msToFrames(cfg.CalibrateMs, frameMs)
	}
	g.calibrated = g.calibrateFrames == 0

	// Shadow gates project alternative thresholds against the same audio. They
	// never touch the upload path, so a single recording produces the whole
	// threshold trade-off curve rather than one point.
	if cfg.MeasureOnly {
		for _, t := range cfg.SweepThresholds {
			if t <= 0 {
				continue
			}
			shadow := cfg
			shadow.SweepThresholds = nil
			shadow.Threshold = t
			shadow.AutoCalibrate = false
			g.shadows = append(g.shadows, newSilenceGate(shadow))
		}
	}
	return g
}

// Filter accepts an arbitrarily sized PCM chunk and returns the bytes that
// should be uploaded, which may be empty. Bytes that do not complete a frame
// are held until the next call.
//
// In measure-only mode every frame is still classified, so the summary reports
// what the savings would have been, but the chunk is returned untouched.
func (g *silenceGate) Filter(chunk []byte) []byte {
	if !g.enabled || g.frameBytes <= 0 {
		return chunk
	}
	g.pending = append(g.pending, chunk...)

	var out []byte
	consumed := 0
	for consumed+g.frameBytes <= len(g.pending) {
		frame := g.pending[consumed : consumed+g.frameBytes]
		kept := g.classify(frame)
		if !g.measureOnly {
			out = append(out, kept...)
		}
		for _, s := range g.shadows {
			s.classify(frame)
		}
		consumed += g.frameBytes
	}
	// Slide the unconsumed tail to the front instead of reslicing, so the
	// backing array does not grow without bound across a long session.
	n := copy(g.pending, g.pending[consumed:])
	g.pending = g.pending[:n]

	if g.measureOnly {
		return chunk
	}
	return out
}

// classify decides the fate of exactly one frame.
func (g *silenceGate) classify(frame []byte) []byte {
	g.totalFrames++
	level := frameRMS(frame)
	g.observe(level)
	if g.adaptive {
		g.trackFloor(level)
	}

	// Calibration window: estimate the room's noise floor. Everything is
	// forwarded during it, because the user may already be speaking.
	if !g.calibrated {
		if g.calFrames == 0 || level < g.calMin {
			g.calMin = level
		}
		g.calFrames++
		if g.calFrames >= g.calibrateFrames {
			g.finishCalibration()
		}
		return g.forward(frame)
	}

	if level >= g.threshold {
		g.aboveRun++
		if g.speaking {
			g.speechFrames++
			return g.forward(frame)
		}
		if g.aboveRun >= g.minSpeechFrames {
			g.speaking = true
			g.speechRuns++
			g.speechFrames++
			g.silentRun = 0
			return g.forward(g.drainPreRoll(frame))
		}
		// A candidate, not yet speech. It waits in the pre-roll buffer, and
		// crucially does not reset silentRun: a single click during a long pause
		// must not restart the silence-lead window and undo the saving.
		g.rememberPreRoll(frame)
		return nil
	}

	g.aboveRun = 0
	if g.speaking {
		g.speaking = false
		g.silentRun = 0
	}
	g.silentRun++
	// The lead of a pause is a boundary marker the server needs; the heartbeat
	// keeps a long pause from looking like a dead connection.
	if g.silentRun <= g.keepFrames {
		return g.forward(frame)
	}
	if g.heartbeatEvery > 0 && g.silentRun%g.heartbeatEvery == 0 {
		return g.forward(frame)
	}
	g.rememberPreRoll(frame)
	return nil
}

// observe records level statistics for the session summary, which is the only
// way to choose a threshold for a particular microphone without guessing.
//
// Percentiles matter more than the mean here. Speech and silence form two
// separate clusters, and the threshold belongs in the valley between them; a
// mean falls somewhere in the middle of the two and identifies neither.
func (g *silenceGate) observe(level float64) {
	if g.totalFrames == 1 || level < g.minLevel {
		g.minLevel = level
	}
	if level > g.maxLevel {
		g.maxLevel = level
	}
	g.sumLevel += level
	g.levels = append(g.levels, level)
}

// trackFloor maintains the noise floor and re-derives the threshold from it.
//
// The floor falls instantly to any quieter frame but rises very slowly. That
// asymmetry is the whole trick. A symmetric estimator, including a plain
// trailing minimum, is dragged upward by speech whenever the measurement window
// happens to contain nothing but speech — which is exactly the situation at the
// start of a recording, and it pushes the threshold above real speech so every
// frame is discarded. Falling fast and rising slowly means genuine silence
// corrects the floor immediately, while speech can never lift it fast enough to
// matter.
//
// The floor starts at zero, so the threshold begins at MinThreshold: permissive.
// Failing open costs a little upload; failing closed loses the user's words.
func (g *silenceGate) trackFloor(level float64) {
	if level < g.floorCurrent {
		g.floorCurrent = level
	} else {
		g.floorCurrent += (level - g.floorCurrent) * floorRiseRate
	}

	derived := g.floorCurrent * g.noiseFactor
	if derived < g.minThreshold {
		derived = g.minThreshold
	}
	if derived > g.maxThreshold {
		derived = g.maxThreshold
	}
	g.threshold = derived
}

// finishCalibration derives a threshold from the one-shot calibration window.
// Retained for the legacy AutoCalibrate path; Adaptive supersedes it.
//
// The floor is estimated from the quietest frame in the window, not the mean.
// A user who starts talking immediately makes the mean a speech measurement,
// and multiplying that by noiseFactor produced a threshold above all real
// speech, which silently discarded the entire recording. Even continuous
// speech leaves quiet frames between syllables, so the minimum stays close to
// the true floor.
//
// maxThreshold is a second, independent guard: speech sits near 0.05-0.3, so a
// noise-derived threshold above that ceiling can only be a mis-measurement.
func (g *silenceGate) finishCalibration() {
	g.calibrated = true
	candidate := g.calMin * g.noiseFactor
	if candidate > g.maxThreshold {
		candidate = g.maxThreshold
	}
	if candidate > g.threshold {
		g.threshold = candidate
	}
}

// forward accounts for frames leaving the gate and returns them unchanged.
func (g *silenceGate) forward(payload []byte) []byte {
	g.forwardedFrames += len(payload) / g.frameBytes
	return payload
}

// rememberPreRoll retains a dropped frame so a following speech onset can
// recover the quiet ramp-up that preceded it.
func (g *silenceGate) rememberPreRoll(frame []byte) {
	if g.preRollFrames <= 0 {
		return
	}
	kept := make([]byte, len(frame))
	copy(kept, frame)
	if len(g.preBuf) >= g.preRollFrames {
		g.preBuf = append(g.preBuf[:0], g.preBuf[len(g.preBuf)-g.preRollFrames+1:]...)
	}
	g.preBuf = append(g.preBuf, kept)
}

// drainPreRoll returns the retained frames followed by the triggering frame,
// and empties the buffer.
func (g *silenceGate) drainPreRoll(frame []byte) []byte {
	if len(g.preBuf) == 0 {
		return frame
	}
	out := make([]byte, 0, (len(g.preBuf)+1)*g.frameBytes)
	for _, f := range g.preBuf {
		out = append(out, f...)
	}
	out = append(out, frame...)
	g.preBuf = g.preBuf[:0]
	return out
}

// Projection is one shadow gate's outcome for an alternative threshold.
type Projection struct {
	Threshold     float64
	WithheldRatio float64
	SpeechFrames  int
	// Runs counts how many separate speech stretches the threshold produced. A
	// threshold set too high fragments continuous speech into many short runs,
	// and each run pays pre-roll and silence-lead overhead, so a high count
	// signals both wasted upload and clipped words.
	Runs int
}

// Projections reports what each swept threshold would have withheld.
func (g *silenceGate) Projections() []Projection {
	out := make([]Projection, 0, len(g.shadows))
	for _, s := range g.shadows {
		ss := s.Summarize()
		out = append(out, Projection{
			Threshold:     s.threshold,
			WithheldRatio: ss.WithheldRatio,
			SpeechFrames:  ss.SpeechFrames,
			Runs:          s.speechRuns,
		})
	}
	return out
}

// Summary reports what the gate did, including the observed level
// distribution. The percentiles matter as much as the savings: they are what a
// threshold should be chosen against for a given microphone.
type Summary struct {
	TotalFrames     int
	ForwardedFrames int
	SpeechFrames    int
	SpeechRuns      int
	WithheldRatio   float64
	Threshold       float64
	NoiseFloor      float64
	MinLevel        float64
	MeanLevel       float64
	MaxLevel        float64
	P10             float64
	P25             float64
	P50             float64
	P75             float64
	P90             float64
}

// percentile returns the value at the given fraction of a sorted slice.
func percentile(sorted []float64, fraction float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(fraction * float64(len(sorted)))
	if i >= len(sorted) {
		i = len(sorted) - 1
	}
	return sorted[i]
}

// Summarize returns the session's gate statistics.
func (g *silenceGate) Summarize() Summary {
	s := Summary{
		TotalFrames:     g.totalFrames,
		ForwardedFrames: g.forwardedFrames,
		SpeechFrames:    g.speechFrames,
		SpeechRuns:      g.speechRuns,
		Threshold:       g.threshold,
		NoiseFloor:      g.floorCurrent,
		MinLevel:        g.minLevel,
		MaxLevel:        g.maxLevel,
	}
	if g.totalFrames > 0 {
		s.WithheldRatio = float64(g.totalFrames-g.forwardedFrames) / float64(g.totalFrames)
		s.MeanLevel = g.sumLevel / float64(g.totalFrames)
	}
	if len(g.levels) > 0 {
		sorted := make([]float64, len(g.levels))
		copy(sorted, g.levels)
		sort.Float64s(sorted)
		s.P10 = percentile(sorted, 0.10)
		s.P25 = percentile(sorted, 0.25)
		s.P50 = percentile(sorted, 0.50)
		s.P75 = percentile(sorted, 0.75)
		s.P90 = percentile(sorted, 0.90)
	}
	return s
}
