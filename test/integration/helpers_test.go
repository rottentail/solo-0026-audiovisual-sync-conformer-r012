package integration

import (
	"testing"

	"github.com/rottentail/solo-0026-audiovisual-sync-conformer/internal/media"
	"github.com/rottentail/solo-0026-audiovisual-sync-conformer/test/synctest"
)

// stripFlash re-emits the fixture video with uniform luma so the picture flash
// detector cannot find an event. The Y4M geometry and frame count are preserved
// and the resulting stream still passes admission probing.
func stripFlash(t *testing.T, video []byte, spec synctest.FixtureSpec) []byte {
	t.Helper()
	clip, err := media.InspectVideo(video)
	if err != nil {
		t.Fatalf("inspect video: %v", err)
	}
	planeSize := clip.Info.Width * clip.Info.Height
	for _, frame := range clip.Frames {
		for offset := 0; offset < planeSize; offset++ {
			frame[offset] = 40
		}
	}
	output, err := media.EncodeY4M(clip.Info, clip.Frames)
	if err != nil {
		t.Fatalf("re-encode flat video: %v", err)
	}
	return output
}

// brightestFrameIndex returns the index of the frame with the highest mean luma.
func brightestFrameIndex(frames [][]byte, planeSize int) int {
	bestIndex, bestMean := 0, -1.0
	for index, frame := range frames {
		sum := 0
		for _, value := range frame[:planeSize] {
			sum += int(value)
		}
		if mean := float64(sum) / float64(planeSize); mean > bestMean {
			bestMean, bestIndex = mean, index
		}
	}
	return bestIndex
}
