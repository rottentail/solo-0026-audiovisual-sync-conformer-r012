package synctest

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rottentail/solo-0026-audiovisual-sync-conformer/internal/domain"
	"github.com/rottentail/solo-0026-audiovisual-sync-conformer/internal/media"
)

// BundleVerification captures the independently measured facts about a
// completed conform so a test can assert every cross-reference itself instead
// of trusting the service's own record.
type BundleVerification struct {
	EntryNames      []string
	Receipt         domain.ConformReceipt
	Measured        domain.Measurement
	AudioSampleRate int
	AudioChannels   int
	DetectedShift   int // signed PCM frame shift between input and aligned WAV
	VideoFrameCount int
	VideoFPSNum     int
	VideoFPSDen     int
	AlignedDigest   string
	VideoDigest     string
	BundleDigest    string
	ResidualMS      int
	exportedVideo   []byte
}

// ReadBundle opens a downloaded ZIP bundle into its named entries.
func ReadBundle(data []byte) (map[string][]byte, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("open bundle: %w", err)
	}
	entries := make(map[string][]byte)
	for _, entry := range reader.File {
		if strings.Contains(entry.Name, "..") || strings.HasPrefix(entry.Name, "/") {
			return nil, fmt.Errorf("bundle entry path is unsafe: %q", entry.Name)
		}
		file, err := entry.Open()
		if err != nil {
			return nil, fmt.Errorf("open bundle entry %q: %w", entry.Name, err)
		}
		content, err := readAllClose(file)
		if err != nil {
			return nil, err
		}
		entries[entry.Name] = content
	}
	return entries, nil
}

// VerifyBundle independently re-decodes every bundle entry and cross-checks it
// against the completed job record, the ready analysis record, and the original
// take media. Any disagreement between a persisted record and the bytes that
// were actually exported is returned as an error, which makes this the
// regression guard for analysis-record/export inconsistency.
func VerifyBundle(
	bundle []byte,
	conform domain.ConformJob,
	analysis domain.SyncAnalysis,
	inputAudio, inputVideo []byte,
) (BundleVerification, error) {
	entries, err := ReadBundle(bundle)
	if err != nil {
		return BundleVerification{}, err
	}

	wav, ok := entries["audio-aligned.wav"]
	if !ok || len(wav) == 0 {
		return BundleVerification{}, fmt.Errorf("bundle is missing a non-empty audio-aligned.wav")
	}
	y4m, ok := entries["video-conformed.y4m"]
	if !ok || len(y4m) == 0 {
		return BundleVerification{}, fmt.Errorf("bundle is missing a non-empty video-conformed.y4m")
	}
	rawReceipt, ok := entries["conform-receipt.json"]
	if !ok || len(rawReceipt) == 0 {
		return BundleVerification{}, fmt.Errorf("bundle is missing a non-empty conform-receipt.json")
	}

	var receipt domain.ConformReceipt
	if err := json.Unmarshal(rawReceipt, &receipt); err != nil {
		return BundleVerification{}, fmt.Errorf("decode embedded receipt: %w", err)
	}

	// Re-decode the exported media through the same production probes a
	// finishing engineer would use, rather than trusting any digest field.
	alignedClip, err := media.InspectAudio(wav)
	if err != nil {
		return BundleVerification{}, fmt.Errorf("probe exported audio: %w", err)
	}
	videoClip, err := media.InspectVideo(y4m)
	if err != nil {
		return BundleVerification{}, fmt.Errorf("probe exported video: %w", err)
	}
	inputClip, err := media.InspectAudio(inputAudio)
	if err != nil {
		return BundleVerification{}, fmt.Errorf("probe input audio: %w", err)
	}
	inputVideoClip, err := media.InspectVideo(inputVideo)
	if err != nil {
		return BundleVerification{}, fmt.Errorf("probe input video: %w", err)
	}

	// Re-run event detection on the exported pair: a correct conform must drive
	// the residual audio/picture offset to within one picture frame period.
	measured, err := media.AnalyzeSync(wav, y4m)
	if err != nil {
		return BundleVerification{}, fmt.Errorf("re-detect events in exported bundle: %w", err)
	}

	shift, err := DetectPCMShift(inputClip.Samples, alignedClip.Samples, alignedClip.Info.Channels)
	if err != nil {
		return BundleVerification{}, err
	}

	result := BundleVerification{
		EntryNames:      []string{"audio-aligned.wav", "video-conformed.y4m", "conform-receipt.json"},
		Receipt:         receipt,
		Measured:        measured,
		AudioSampleRate: alignedClip.Info.SampleRate,
		AudioChannels:   alignedClip.Info.Channels,
		DetectedShift:   shift,
		VideoFrameCount: videoClip.Info.FrameCount,
		VideoFPSNum:     videoClip.Info.FPSNum,
		VideoFPSDen:     videoClip.Info.FPSDen,
		AlignedDigest:   media.DigestBytes(wav),
		VideoDigest:     media.DigestBytes(y4m),
		BundleDigest:    media.DigestBytes(bundle),
		ResidualMS:      measured.OffsetMS,
		exportedVideo:   y4m,
	}
	if err := result.crossCheck(conform, analysis, inputClip, inputVideoClip); err != nil {
		return result, err
	}
	return result, nil
}

func (v BundleVerification) crossCheck(
	conform domain.ConformJob,
	analysis domain.SyncAnalysis,
	inputClip media.AudioClip,
	inputVideoClip media.VideoClip,
) error {
	// 1. Bundle digest and byte count must describe the bytes actually served.
	if conform.BundleDigest != v.BundleDigest {
		return fmt.Errorf(
			"conform record digest %q does not match the served bundle digest %q",
			conform.BundleDigest, v.BundleDigest,
		)
	}
	if conform.BundleBytes <= 0 {
		return fmt.Errorf("conform record has no bundle byte count")
	}

	// 2. Receipt fields must agree with the conform job and the analysis.
	if receipt := v.Receipt; receipt.ConformID != conform.ID ||
		receipt.AnalysisID != conform.AnalysisID ||
		receipt.TakeID != conform.TakeID {
		return fmt.Errorf("receipt identity (%s/%s/%s) does not match the conform job",
			receipt.ConformID, receipt.AnalysisID, receipt.TakeID)
	}
	if analysis.OffsetMS == nil {
		return fmt.Errorf("analysis record has no measured offset")
	}
	if v.Receipt.MeasuredOffsetMS != *analysis.OffsetMS ||
		v.Receipt.AppliedOffsetMS != conform.AppliedOffsetMS ||
		v.Receipt.AppliedOffsetMS != *analysis.OffsetMS {
		return fmt.Errorf(
			"receipt correction (measured=%d applied=%d) disagrees with analysis offset %d and job offset %d",
			v.Receipt.MeasuredOffsetMS, v.Receipt.AppliedOffsetMS, *analysis.OffsetMS, conform.AppliedOffsetMS,
		)
	}
	if v.Receipt.InputTakeDigest == "" {
		return fmt.Errorf("receipt is missing the input take digest")
	}

	// 3. The sample correction recorded must be the exact conversion applied to
	// the exported WAV, measured frame-by-frame rather than trusted.
	expectedSamples := ExpectedAppliedSamples(v.AudioSampleRate, v.Receipt.AppliedOffsetMS)
	if v.Receipt.AppliedSamples != expectedSamples {
		return fmt.Errorf(
			"receipt applied_samples %d does not match the %d-sample conversion of %d ms @ %d Hz",
			v.Receipt.AppliedSamples, expectedSamples, v.Receipt.AppliedOffsetMS, v.AudioSampleRate,
		)
	}
	if v.DetectedShift != expectedSamples {
		return fmt.Errorf(
			"exported WAV actually moved %d PCM frames but the receipt claims %d frames",
			v.DetectedShift, expectedSamples,
		)
	}
	if conform.AppliedSamples != v.Receipt.AppliedSamples {
		return fmt.Errorf(
			"conform record applied_samples %d disagrees with receipt %d",
			conform.AppliedSamples, v.Receipt.AppliedSamples,
		)
	}

	// 4. Embedded digests must describe the actual entry bytes.
	if v.Receipt.AudioDigest != v.AlignedDigest || conform.AudioOutputDigest != v.AlignedDigest {
		return fmt.Errorf("audio digest fields do not match the exported WAV bytes")
	}
	if v.Receipt.VideoDigest != v.VideoDigest || conform.VideoOutputDigest != v.VideoDigest {
		return fmt.Errorf("video digest fields do not match the exported Y4M bytes")
	}

	// 5. The picture must remain identity-timed: same frame count, geometry and
	// frame rate as the admitted take, with no frame reordering.
	if v.VideoFrameCount != inputVideoClip.Info.FrameCount {
		return fmt.Errorf(
			"exported picture has %d frames but the admitted take has %d",
			v.VideoFrameCount, inputVideoClip.Info.FrameCount,
		)
	}
	if v.VideoFPSNum != inputVideoClip.Info.FPSNum || v.VideoFPSDen != inputVideoClip.Info.FPSDen {
		return fmt.Errorf("exported picture frame rate was altered by the conform")
	}
	if flashFrameMoved(inputVideoClip, v.exportedVideo) {
		return fmt.Errorf("exported picture frames were reordered: flash frame no longer matches the input")
	}

	// 6. Sample rate and channel count must be preserved.
	if v.AudioSampleRate != inputClip.Info.SampleRate || v.AudioChannels != inputClip.Info.Channels {
		return fmt.Errorf("exported WAV changed sample rate or channel layout")
	}

	// 7. The exported pair must actually be aligned: residual within one
	// picture frame period plus a one-millisecond detection quantization band.
	framePeriodMS := 1000 * v.VideoFPSDen / v.VideoFPSNum
	tolerance := framePeriodMS + 1
	if abs(v.ResidualMS) > tolerance {
		return fmt.Errorf(
			"exported audio/picture residual is %d ms, beyond the %d ms tolerance (record claims a %d ms correction)",
			v.ResidualMS, tolerance, v.Receipt.AppliedOffsetMS,
		)
	}
	return nil
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

type readCloser interface {
	Read(p []byte) (int, error)
	Close() error
}

func readAllClose(file readCloser) ([]byte, error) {
	defer file.Close()
	buffer := new(bytes.Buffer)
	if _, err := buffer.ReadFrom(file); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}
