package media

import (
	"testing"

	"github.com/rottentail/solo-0026-audiovisual-sync-conformer/internal/domain"
)

func TestEncodeDecodePCM16RoundTrip(t *testing.T) {
	rates := []int{8000, 44100, 48000, 96000}
	for _, rate := range rates {
		for _, channels := range []int{1, 2} {
			samples := []int16{0, 1, -1, 32767, -32768, 12345, -12345}
			if channels == 2 {
				samples = append(samples, 0, -9, 9, -32768, 32767, 1, -1)
			}
			data, err := EncodePCM16(rate, channels, samples)
			if err != nil {
				t.Fatalf("encode %dHz/%dch: %v", rate, channels, err)
			}
			clip, err := InspectAudio(data)
			if err != nil {
				t.Fatalf("decode %dHz/%dch: %v", rate, channels, err)
			}
			if clip.Info.SampleRate != rate || clip.Info.Channels != channels {
				t.Fatalf("descriptor mismatch: got %dHz/%dch", clip.Info.SampleRate, clip.Info.Channels)
			}
			if len(clip.Samples) != len(samples) {
				t.Fatalf("sample count mismatch: got %d want %d", len(clip.Samples), len(samples))
			}
			for index, sample := range samples {
				if clip.Samples[index] != sample {
					t.Fatalf("sample %d mismatch: got %d want %d", index, clip.Samples[index], sample)
				}
			}
		}
	}
}

func TestInspectAudioRejectsMalformedChunks(t *testing.T) {
	t.Run("not riff", func(t *testing.T) {
		if _, err := InspectAudio([]byte("not a wav file at all")); err == nil {
			t.Fatal("expected rejection of non-RIFF payload")
		}
	})
	t.Run("truncated riff size", func(t *testing.T) {
		data, _ := EncodePCM16(48000, 1, make([]int16, 16))
		data[4] = 0xff
		data[5] = 0xff
		if _, err := InspectAudio(data); err == nil {
			t.Fatal("expected rejection of inflated RIFF size")
		}
	})
	t.Run("non pcm", func(t *testing.T) {
		data, _ := EncodePCM16(48000, 1, make([]int16, 16))
		data[20] = 2 // flip audio format from PCM to something else
		if _, err := InspectAudio(data); err == nil {
			t.Fatal("expected rejection of non-PCM format")
		}
	})
	t.Run("bad block align", func(t *testing.T) {
		data, _ := EncodePCM16(48000, 2, make([]int16, 16))
		data[32] = 3 // block align must be 4 for stereo 16-bit
		if _, err := InspectAudio(data); err == nil {
			t.Fatal("expected rejection of inconsistent block alignment")
		}
	})
}

func TestInspectVideoRejectsTruncation(t *testing.T) {
	info := domain.VideoInfo{Width: 16, Height: 16, FPSNum: 24, FPSDen: 1, FrameCount: 2, Chroma: "C420jpeg"}
	frameBytes := 16*16 + 2*8*8
	frames := [][]byte{make([]byte, frameBytes), make([]byte, frameBytes)}
	data, err := EncodeY4M(info, frames)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := InspectVideo(data); err != nil {
		t.Fatalf("valid Y4M rejected: %v", err)
	}
	// Drop the final frame body.
	if _, err := InspectVideo(data[:len(data)-frameBytes]); err == nil {
		t.Fatal("expected rejection of truncated final frame")
	}
	// Corrupt the header magic.
	corrupted := append([]byte(nil), data...)
	copy(corrupted[0:8], "BADMAGIC")
	if _, err := InspectVideo(corrupted); err == nil {
		t.Fatal("expected rejection of non-Y4M header")
	}
}

func TestInspectVideoRejectsOddDimensions(t *testing.T) {
	info := domain.VideoInfo{Width: 17, Height: 16, FPSNum: 24, FPSDen: 1, FrameCount: 1, Chroma: "C420jpeg"}
	if _, err := EncodeY4M(info, [][]byte{make([]byte, 17*16+2*8*8)}); err == nil {
		t.Fatal("encoder should reject odd C420 width")
	}
}
