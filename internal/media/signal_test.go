package media

import (
	"testing"

	"github.com/rottentail/solo-0026-audiovisual-sync-conformer/internal/domain"
)

// flashVideo builds a dark stream with one bright frame at flashIndex.
func flashVideo(t *testing.T, fpsNum, fpsDen, frames, flashIndex, width, height int) []byte {
	t.Helper()
	frameBytes := width*height + 2*(width/2)*(height/2)
	encoded := make([][]byte, 0, frames)
	for index := 0; index < frames; index++ {
		frame := make([]byte, frameBytes)
		luma := byte(24)
		if index == flashIndex {
			luma = byte(230)
		}
		for offset := 0; offset < width*height; offset++ {
			frame[offset] = luma
		}
		encoded = append(encoded, frame)
	}
	data, err := EncodeY4M(domain.VideoInfo{
		Width: width, Height: height, FPSNum: fpsNum, FPSDen: fpsDen,
		FrameCount: frames, Chroma: "C420jpeg",
	}, encoded)
	if err != nil {
		t.Fatalf("encode y4m: %v", err)
	}
	return data
}

func TestDetectPictureFlashFractionalFrameRates(t *testing.T) {
	cases := []struct {
		name       string
		fpsNum     int
		fpsDen     int
		flashFrame int
		wantMS     int
	}{
		{"integer 24fps frame 9", 24, 1, 9, 375},
		{"integer 30fps frame 12", 30, 1, 12, 400},
		{"ntsc 30000/1001 frame 10", 30000, 1001, 10, 333},
		{"ntsc 24000/1001 frame 9", 24000, 1001, 9, 375},
		{"ntsc 60000/1001 frame 15", 60000, 1001, 15, 250},
		{"first frame flash at 30000/1001", 30000, 1001, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			video := flashVideo(t, tc.fpsNum, tc.fpsDen, 24, tc.flashFrame, 32, 16)
			clip, err := InspectVideo(video)
			if err != nil {
				t.Fatalf("inspect: %v", err)
			}
			eventMS, prominence, err := detectPictureFlash(clip)
			if err != nil {
				t.Fatalf("detect flash: %v", err)
			}
			if eventMS != tc.wantMS {
				t.Fatalf("flash at %d ms, want %d ms (frame %d @ %d/%d)",
					eventMS, tc.wantMS, tc.flashFrame, tc.fpsNum, tc.fpsDen)
			}
			if prominence <= 0 {
				t.Fatalf("flash prominence must be positive, got %.3f", prominence)
			}
		})
	}
}

func TestDetectPictureFlashRejectsFlatStream(t *testing.T) {
	// Build a stream with no bright frame.
	frameBytes := 32*16 + 2*16*8
	frames := make([][]byte, 12)
	for index := range frames {
		frame := make([]byte, frameBytes)
		for offset := 0; offset < 32*16; offset++ {
			frame[offset] = 40
		}
		frames[index] = frame
	}
	data, err := EncodeY4M(domain.VideoInfo{
		Width: 32, Height: 16, FPSNum: 24, FPSDen: 1, FrameCount: 12, Chroma: "C420jpeg",
	}, frames)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	clip, err := InspectVideo(data)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if _, _, err := detectPictureFlash(clip); err == nil {
		t.Fatal("flat luma stream must not produce a flash event")
	}
}

func TestAnalyzeSyncKnownOffsets(t *testing.T) {
	// Reuse the production calibration pair whose ground truth is documented.
	audio, video, err := CalibrationPair()
	if err != nil {
		t.Fatalf("calibration pair: %v", err)
	}
	measurement, err := AnalyzeSync(audio, video)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if measurement.AudioEventMS != CalibrationAudioEvent {
		t.Fatalf("audio event %d ms, want %d", measurement.AudioEventMS, CalibrationAudioEvent)
	}
	wantVideo := CalibrationVideoEvent * 1000 / CalibrationFPS
	if measurement.VideoEventMS != wantVideo {
		t.Fatalf("video event %d ms, want %d", measurement.VideoEventMS, wantVideo)
	}
	if measurement.OffsetMS != CalibrationAudioEvent-wantVideo {
		t.Fatalf("offset %d ms, want %d", measurement.OffsetMS, CalibrationAudioEvent-wantVideo)
	}
	if measurement.Confidence <= 0 || measurement.AudioProminence <= 0 || measurement.VideoProminence <= 0 {
		t.Fatalf("measurement must carry positive prominence: %+v", measurement)
	}
}
