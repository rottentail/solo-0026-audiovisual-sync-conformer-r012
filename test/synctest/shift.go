package synctest

import (
	"fmt"

	"github.com/rottentail/solo-0026-audiovisual-sync-conformer/internal/media"
)

// DetectPCMShift independently measures the signed PCM frame shift applied
// between an input track and an aligned track. A positive shift means the
// output at frame n equals the input at frame n+shift (audio moved earlier),
// which is exactly the convention the conformer uses for a positive offset.
//
// Measurement strategy: find the first frame whose signal is clearly above the
// silence floor in each track and compare the positions. The tracks share the
// same sample rate, channel count and frame count, and the fixture has a single
// transient, so the onset-frame difference is the applied correction.
func DetectPCMShift(input, output []int16, channels int) (int, error) {
	if channels != 1 && channels != 2 {
		return 0, fmt.Errorf("unsupported channel count %d", channels)
	}
	if len(input) != len(output) {
		return 0, fmt.Errorf("aligned track length %d differs from input length %d", len(output), len(input))
	}
	inputOnset, err := firstLoudFrame(input, channels)
	if err != nil {
		return 0, fmt.Errorf("input track: %w", err)
	}
	outputOnset, err := firstLoudFrame(output, channels)
	if err != nil {
		return 0, fmt.Errorf("aligned track: %w", err)
	}
	// output[outputOnset] == input[inputOnset], so input index = output index + shift.
	return inputOnset - outputOnset, nil
}

// firstLoudFrame returns the first interleaved-sample frame whose absolute
// amplitude crosses a fixed threshold well above the fixture's zero floor.
func firstLoudFrame(samples []int16, channels int) (int, error) {
	const threshold = 2000
	for frame := 0; frame*channels < len(samples); frame++ {
		for channel := 0; channel < channels; channel++ {
			if int(samples[frame*channels+channel]) > threshold ||
				int(samples[frame*channels+channel]) < -threshold {
				return frame, nil
			}
		}
	}
	return 0, fmt.Errorf("no loud transient found in track")
}

// flashFrameMoved compares the luma profile of the input and exported picture
// streams and fails when the bright frame lands at a different index, which
// would indicate reordering or dropped frames during the conform. The exported
// Y4M bytes are passed in directly so the comparison is independent of the
// integer-millisecond event timestamp (which is lossy at fractional frame
// rates).
func flashFrameMoved(input media.VideoClip, exportedVideo []byte) bool {
	exported, err := media.InspectVideo(exportedVideo)
	if err != nil {
		return true
	}
	if exported.Info.FrameCount != input.Info.FrameCount {
		return true
	}
	return brightestFrame(input) != brightestFrame(exported)
}

// brightestFrame returns the index of the frame with the highest mean luma.
func brightestFrame(clip media.VideoClip) int {
	planeSize := clip.Info.Width * clip.Info.Height
	bestIndex, bestLuma := 0, -1.0
	for index, frame := range clip.Frames {
		sum := 0
		for _, value := range frame[:planeSize] {
			sum += int(value)
		}
		mean := float64(sum) / float64(planeSize)
		if mean > bestLuma {
			bestLuma, bestIndex = mean, index
		}
	}
	return bestIndex
}
