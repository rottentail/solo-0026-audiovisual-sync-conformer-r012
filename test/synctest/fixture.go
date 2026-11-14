// Package synctest provides deterministic calibration media and verification
// helpers for the audiovisual sync conformer test suites.
//
// The fixtures are built independently of the production calibration pair so
// the tests do not merely confirm a constant against itself. Every fixture has
// a known, parameterized construction: a configurable audio sample rate,
// channel count, frame rate (including fractional rates), and a known signed
// offset between the audio transient and the picture flash.
package synctest

import (
	"fmt"
	"math"

	"github.com/rottentail/solo-0026-audiovisual-sync-conformer/internal/domain"
	"github.com/rottentail/solo-0026-audiovisual-sync-conformer/internal/media"
)

// FixtureSpec describes one known-offset calibration take.
type FixtureSpec struct {
	SampleRate int // PCM sample rate in Hz
	Channels   int // 1 or 2
	FPSNum     int // Y4M frame rate numerator
	FPSDen     int // Y4M frame rate denominator (1001 for NTSC fractional rates)
	Frames     int // number of picture frames
	Width      int // picture width, must be even
	Height     int // picture height, must be even
	FlashFrame int // index of the single bright picture frame
	// AudioEventMS is the transient start time in milliseconds relative to the
	// start of the take. The picture event is derived from FlashFrame and the
	// frame rate, so the signed offset is fully controlled by the caller.
	AudioEventMS int
}

// Fixture is a generated calibration take plus the ground-truth timing used to
// judge the production analysis and conform results.
type Fixture struct {
	Spec         FixtureSpec
	Audio        []byte
	Video        []byte
	AudioEventMS int
	// VideoEventMS is the integer-millisecond timestamp the production detector
	// is expected to report for the flash frame.
	VideoEventMS int
	// OffsetMS is the signed known offset (audio late is positive).
	OffsetMS int
}

// DefaultSpec returns a spec with production-reasonable defaults.
func DefaultSpec() FixtureSpec {
	return FixtureSpec{
		SampleRate:   48000,
		Channels:     1,
		FPSNum:       24,
		FPSDen:       1,
		Frames:       30,
		Width:        64,
		Height:       36,
		FlashFrame:   9,
		AudioEventMS: 500,
	}
}

// BuildFixture generates a WAV/Y4M pair from the spec.
func BuildFixture(spec FixtureSpec) (Fixture, error) {
	if err := validateSpec(spec); err != nil {
		return Fixture{}, err
	}

	audio := buildAudio(spec)
	video, err := buildVideo(spec)
	if err != nil {
		return Fixture{}, err
	}
	videoEventMS := spec.FlashFrame * 1000 * spec.FPSDen / spec.FPSNum
	return Fixture{
		Spec:         spec,
		Audio:        audio,
		Video:        video,
		AudioEventMS: spec.AudioEventMS,
		VideoEventMS: videoEventMS,
		OffsetMS:     spec.AudioEventMS - videoEventMS,
	}, nil
}

func validateSpec(spec FixtureSpec) error {
	if spec.SampleRate < 8000 || spec.SampleRate > 192000 {
		return fmt.Errorf("sample rate %d outside accepted range", spec.SampleRate)
	}
	if spec.Channels != 1 && spec.Channels != 2 {
		return fmt.Errorf("channels must be 1 or 2, got %d", spec.Channels)
	}
	if spec.FPSNum <= 0 || spec.FPSDen <= 0 {
		return fmt.Errorf("frame rate must be positive")
	}
	if spec.Frames <= 0 || spec.FlashFrame < 0 || spec.FlashFrame >= spec.Frames {
		return fmt.Errorf("flash frame must be inside the picture stream")
	}
	if spec.Width <= 0 || spec.Height <= 0 || spec.Width%2 != 0 || spec.Height%2 != 0 {
		return fmt.Errorf("C420 dimensions must be positive and even")
	}
	if spec.AudioEventMS < 40 {
		return fmt.Errorf("audio event must leave a quiet pre-roll for onset detection")
	}
	return nil
}

// buildAudio synthesizes a silent track containing one decaying 440 Hz
// transient starting at the requested millisecond. The sample count always
// covers the full picture duration so the conformer never runs out of samples.
func buildAudio(spec FixtureSpec) []byte {
	pictureDurationMS := spec.Frames * 1000 * spec.FPSDen / spec.FPSNum
	trackMS := pictureDurationMS + 600
	frameCount := spec.SampleRate * trackMS / 1000
	samples := make([]int16, frameCount*spec.Channels)
	eventSample := spec.SampleRate * spec.AudioEventMS / 1000
	for frame := eventSample; frame < frameCount; frame++ {
		elapsed := float64(frame-eventSample) / float64(spec.SampleRate)
		envelope := math.Exp(-elapsed * 6)
		value := math.Sin(2*math.Pi*440*float64(frame-eventSample)/float64(spec.SampleRate)) *
			16000 * envelope
		sample := int16(math.Round(value))
		for channel := 0; channel < spec.Channels; channel++ {
			samples[frame*spec.Channels+channel] = sample
		}
	}
	audio, err := media.EncodePCM16(spec.SampleRate, spec.Channels, samples)
	if err != nil {
		panic(fmt.Sprintf("fixture audio encoding failed: %v", err))
	}
	return audio
}

// buildVideo synthesizes a dark C420 stream with exactly one bright frame.
func buildVideo(spec FixtureSpec) ([]byte, error) {
	frameBytes := spec.Width*spec.Height + 2*(spec.Width/2)*(spec.Height/2)
	frames := make([][]byte, 0, spec.Frames)
	for index := 0; index < spec.Frames; index++ {
		frame := make([]byte, frameBytes)
		luma := byte(28)
		if index == spec.FlashFrame {
			luma = byte(224)
		}
		for y := 0; y < spec.Height; y++ {
			for x := 0; x < spec.Width; x++ {
				frame[y*spec.Width+x] = luma
			}
		}
		chromaSize := (spec.Width / 2) * (spec.Height / 2)
		for offset := 0; offset < chromaSize; offset++ {
			frame[spec.Width*spec.Height+offset] = 128
			frame[spec.Width*spec.Height+chromaSize+offset] = 128
		}
		frames = append(frames, frame)
	}
	info := domain.VideoInfo{
		Width:      spec.Width,
		Height:     spec.Height,
		FPSNum:     spec.FPSNum,
		FPSDen:     spec.FPSDen,
		FrameCount: spec.Frames,
		Chroma:     "C420jpeg",
	}
	return media.EncodeY4M(info, frames)
}

// ExpectedAppliedSamples mirrors the production sample conversion so tests can
// assert the receipt describes exactly the correction applied to the WAV.
func ExpectedAppliedSamples(sampleRate, measuredOffsetMS int) int {
	return int(math.Round(float64(measuredOffsetMS) * float64(sampleRate) / 1000))
}
