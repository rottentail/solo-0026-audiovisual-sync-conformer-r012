package media

import (
	"testing"

	"github.com/rottentail/solo-0026-audiovisual-sync-conformer/internal/domain"
)

// toneTrack builds a silent track with one loud transient starting at
// eventFrame, used to measure the shift applied by AlignAudio directly.
func toneTrack(t *testing.T, sampleRate, channels, totalFrames, eventFrame int) []byte {
	t.Helper()
	samples := make([]int16, totalFrames*channels)
	for frame := eventFrame; frame < totalFrames; frame++ {
		value := int16(20000)
		for channel := 0; channel < channels; channel++ {
			samples[frame*channels+channel] = value
		}
	}
	data, err := EncodePCM16(sampleRate, channels, samples)
	if err != nil {
		t.Fatalf("encode tone: %v", err)
	}
	return data
}

func firstToneFrame(samples []int16, channels int) int {
	for frame := 0; frame*channels < len(samples); frame++ {
		if samples[frame*channels] > 10000 {
			return frame
		}
	}
	return -1
}

func TestAlignAudioSignedCorrections(t *testing.T) {
	cases := []struct {
		name      string
		rate      int
		channels  int
		offsetMS  int
		ceilingMS int
		wantShift int
	}{
		{"positive offset 48k mono", 48000, 1, 125, 250, 6000},
		{"negative offset 48k mono", 48000, 1, -125, 250, -6000},
		{"44.1k positive offset rounding", 44100, 1, 100, 250, 4410},
		{"44.1k fractional-sample rounding 33ms", 44100, 1, 33, 250, 1455},
		{"96k stereo negative offset", 96000, 2, -50, 250, -4800},
		{"8k positive offset", 8000, 1, 200, 500, 1600},
		{"zero offset", 48000, 1, 0, 250, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const eventFrame = 24000 // 0.5s at 48k; absolute position irrelevant to shift
			input := toneTrack(t, tc.rate, tc.channels, eventFrame*2, eventFrame)
			aligned, err := AlignAudio(input, tc.offsetMS, tc.ceilingMS)
			if err != nil {
				t.Fatalf("align: %v", err)
			}
			if aligned.AppliedSamples != tc.wantShift {
				t.Fatalf("applied samples %d, want %d", aligned.AppliedSamples, tc.wantShift)
			}
			clip, err := InspectAudio(aligned.WAV)
			if err != nil {
				t.Fatalf("inspect aligned: %v", err)
			}
			if clip.Info.SampleRate != tc.rate || clip.Info.Channels != tc.channels {
				t.Fatalf("alignment changed format to %dHz/%dch", clip.Info.SampleRate, clip.Info.Channels)
			}
			inputClip, _ := InspectAudio(input)
			inputOnset := firstToneFrame(inputClip.Samples, tc.channels)
			outputOnset := firstToneFrame(clip.Samples, tc.channels)
			measuredShift := inputOnset - outputOnset
			if measuredShift != tc.wantShift {
				t.Fatalf("PCM actually shifted %d frames, want %d", measuredShift, tc.wantShift)
			}
			if aligned.Digest != DigestBytes(aligned.WAV) {
				t.Fatal("aligned audio digest does not describe its WAV bytes")
			}
		})
	}
}

func TestAlignAudioEnforcesCeiling(t *testing.T) {
	input := toneTrack(t, 48000, 1, 48000, 24000)
	if _, err := AlignAudio(input, 300, 250); err == nil {
		t.Fatal("correction above ceiling must fail")
	} else if domain.AsError(err).Code != "correction_limit_exceeded" {
		t.Fatalf("unexpected error code %q", domain.AsError(err).Code)
	}
	// A negative offset beyond the ceiling is rejected on magnitude.
	if _, err := AlignAudio(input, -300, 250); err == nil {
		t.Fatal("negative correction above ceiling must fail")
	}
}

func TestNormalizeY4MPreservesGeometryAndFrames(t *testing.T) {
	video := flashVideo(t, 30000, 1001, 18, 6, 48, 24)
	output, info, err := NormalizeY4M(video)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	clip, err := InspectVideo(output)
	if err != nil {
		t.Fatalf("re-inspect: %v", err)
	}
	if clip.Info.FrameCount != 18 || clip.Info.FPSNum != 30000 || clip.Info.FPSDen != 1001 {
		t.Fatalf("geometry changed: %+v", clip.Info)
	}
	if clip.Info.Width != 48 || clip.Info.Height != 24 {
		t.Fatalf("dimensions changed: %dx%d", clip.Info.Width, clip.Info.Height)
	}
	if info.Digest != DigestBytes(output) {
		t.Fatal("normalized video digest does not describe the emitted bytes")
	}
}
