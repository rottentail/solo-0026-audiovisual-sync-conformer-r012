package integration

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/rottentail/solo-0026-audiovisual-sync-conformer/internal/domain"
	"github.com/rottentail/solo-0026-audiovisual-sync-conformer/internal/media"
	"github.com/rottentail/solo-0026-audiovisual-sync-conformer/test/synctest"
)

// happyBundle drives one full conform and returns the verified building blocks
// used by every mutation test below.
func happyBundle(t *testing.T) (fixture synctest.Fixture, conform domain.ConformJob, analysis domain.SyncAnalysis, bundle []byte, h *synctest.Harness) {
	t.Helper()
	fixture, err := synctest.BuildFixture(synctest.DefaultSpec())
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	h = synctest.NewHarness(t)
	h.AdmitTake("take-mut", "mutation take", fixture)
	_, analysis = h.CreateAnalysis("take-mut", "ana-mut", 0.55, 5000)
	if analysis.State != domain.AnalysisStateReady || analysis.OffsetMS == nil {
		t.Fatalf("analysis not ready: %+v", analysis)
	}
	_, conform = h.CreateConform("ana-mut", "cfm-mut", 5000)
	if conform.State != domain.ConformStateCompleted {
		t.Fatalf("conform not completed: %+v", conform)
	}
	status, bundle := h.GetBundle("cfm-mut")
	if status != http.StatusOK {
		t.Fatalf("bundle: %d", status)
	}
	if _, err := synctest.VerifyBundle(bundle, conform, analysis, fixture.Audio, fixture.Video); err != nil {
		t.Fatalf("baseline verification should pass: %v", err)
	}
	return fixture, conform, analysis, bundle, h
}

// mutateBundle rewrites one ZIP entry while preserving the archive structure.
func mutateBundle(t *testing.T, original []byte, entryName string, newContent []byte) []byte {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(original), int64(len(original)))
	if err != nil {
		t.Fatalf("open original bundle: %v", err)
	}
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for _, entry := range reader.File {
		header := &zip.FileHeader{
			Name:     entry.Name,
			Method:   zip.Store,
			Modified: entry.Modified,
		}
		part, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatalf("create mutated entry: %v", err)
		}
		if entry.Name == entryName {
			if _, err := part.Write(newContent); err != nil {
				t.Fatalf("write mutated entry: %v", err)
			}
			continue
		}
		file, err := entry.Open()
		if err != nil {
			t.Fatalf("open entry: %v", err)
		}
		if _, err := io.Copy(part, file); err != nil {
			file.Close()
			t.Fatalf("copy entry: %v", err)
		}
		file.Close()
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close mutated bundle: %v", err)
	}
	return output.Bytes()
}

func decodeJSON(data []byte, target any) error {
	return json.Unmarshal(data, target)
}

func mustEncodeJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("encode json: %v", err)
	}
	return append(data, '\n')
}

func TestVerifierDetectsTamperedAudioExport(t *testing.T) {
	fixture, conform, analysis, bundle, _ := happyBundle(t)

	// Re-encode the audio with an extra 10 ms shift so the exported WAV no
	// longer matches the receipt's applied_samples.
	clip, err := media.InspectAudio(fixture.Audio)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	extra := clip.Info.SampleRate / 100 // 10 ms worth of frames
	tamperedSamples := make([]int16, len(clip.Samples))
	for outputIndex := range tamperedSamples {
		source := outputIndex + extra*clip.Info.Channels
		if source >= 0 && source < len(clip.Samples) {
			tamperedSamples[outputIndex] = clip.Samples[source]
		}
	}
	tamperedWAV, err := media.EncodePCM16(clip.Info.SampleRate, clip.Info.Channels, tamperedSamples)
	if err != nil {
		t.Fatalf("encode tampered audio: %v", err)
	}
	mutated := mutateBundle(t, bundle, "audio-aligned.wav", tamperedWAV)

	// With the stale digest this must fail; and with an honest digest it must
	// still fail on the measured PCM shift, proving the verifier does not rely
	// on digest fields alone.
	if _, err := synctest.VerifyBundle(mutated, conform, analysis, fixture.Audio, fixture.Video); err == nil {
		t.Fatal("verifier accepted a bundle whose exported WAV disagrees with its receipt")
	} else {
		t.Logf("correctly rejected tampered audio (digest): %v", err)
	}
	honestConform := conform
	honestConform.BundleDigest = media.DigestBytes(mutated)
	if _, err := synctest.VerifyBundle(mutated, honestConform, analysis, fixture.Audio, fixture.Video); err == nil {
		t.Fatal("verifier accepted shifted WAV even after the bundle digest was made honest")
	} else {
		t.Logf("correctly rejected tampered audio (pcm shift): %v", err)
	}
}

func TestVerifierDetectsRecordLyingAboutAppliedSamples(t *testing.T) {
	fixture, conform, analysis, bundle, _ := happyBundle(t)

	// Simulate an analysis/record-vs-export inconsistency: the persisted job
	// claims a different sample correction than the bytes actually carry.
	lyingConform := conform
	lyingConform.AppliedSamples = conform.AppliedSamples + 4800
	lyingConform.AppliedOffsetMS = conform.AppliedOffsetMS + 100
	if _, err := synctest.VerifyBundle(bundle, lyingConform, analysis, fixture.Audio, fixture.Video); err == nil {
		t.Fatal("verifier accepted a conform record whose applied samples do not match the export")
	} else {
		t.Logf("correctly rejected lying conform record: %v", err)
	}

	// A lying analysis offset must be caught against the same exported bundle.
	lyingAnalysis := analysis
	wrongOffset := *analysis.OffsetMS + 75
	lyingAnalysis.OffsetMS = &wrongOffset
	if _, err := synctest.VerifyBundle(bundle, conform, lyingAnalysis, fixture.Audio, fixture.Video); err == nil {
		t.Fatal("verifier accepted an analysis whose offset disagrees with the receipt")
	} else {
		t.Logf("correctly rejected lying analysis record: %v", err)
	}
}

func TestVerifierDetectsTamperedReceipt(t *testing.T) {
	fixture, conform, analysis, bundle, _ := happyBundle(t)

	entries, err := synctest.ReadBundle(bundle)
	if err != nil {
		t.Fatalf("read bundle: %v", err)
	}
	var receipt domain.ConformReceipt
	if err := decodeJSON(entries["conform-receipt.json"], &receipt); err != nil {
		t.Fatalf("decode receipt: %v", err)
	}
	receipt.AppliedSamples += 1234
	fakeReceipt := mustEncodeJSON(t, receipt)
	mutated := mutateBundle(t, bundle, "conform-receipt.json", fakeReceipt)

	if _, err := synctest.VerifyBundle(mutated, conform, analysis, fixture.Audio, fixture.Video); err == nil {
		t.Fatal("verifier accepted a bundle whose receipt was tampered with")
	} else {
		t.Logf("correctly rejected tampered receipt: %v", err)
	}
}

func TestVerifierDetectsTamperedVideoFrames(t *testing.T) {
	fixture, conform, analysis, bundle, _ := happyBundle(t)

	// Swap two picture frames in the exported Y4M, preserving frame count and
	// geometry but breaking identity-time picture order.
	clip, err := media.InspectVideo(fixture.Video)
	if err != nil {
		t.Fatalf("inspect video: %v", err)
	}
	if clip.Info.FrameCount < 4 {
		t.Fatal("fixture needs at least four frames for a swap mutation")
	}
	frames := clip.Frames
	// Move the bright flash frame itself so the luma profile (and thus frame
	// order observably) changes while frame count and geometry stay intact.
	flashIndex := brightestFrameIndex(frames, clip.Info.Width*clip.Info.Height)
	targetIndex := 0
	if flashIndex == targetIndex {
		targetIndex = 2
	}
	frames[flashIndex], frames[targetIndex] = frames[targetIndex], frames[flashIndex]
	reordered, err := media.EncodeY4M(clip.Info, frames)
	if err != nil {
		t.Fatalf("re-encode reordered video: %v", err)
	}
	mutated := mutateBundle(t, bundle, "video-conformed.y4m", reordered)

	if _, err := synctest.VerifyBundle(mutated, conform, analysis, fixture.Audio, fixture.Video); err == nil {
		t.Fatal("verifier accepted a bundle whose picture frames were reordered")
	} else {
		t.Logf("correctly rejected reordered picture (digest): %v", err)
	}
	// With an honest bundle digest it must still detect the frame reorder from
	// the decoded luma profile rather than the digest field.
	honestConform := conform
	honestConform.BundleDigest = media.DigestBytes(mutated)
	if _, err := synctest.VerifyBundle(mutated, honestConform, analysis, fixture.Audio, fixture.Video); err == nil {
		t.Fatal("verifier accepted reordered picture after the digest was made honest")
	} else {
		t.Logf("correctly rejected reordered picture (luma): %v", err)
	}
}

func TestVerifierDetectsDigestFieldMismatch(t *testing.T) {
	fixture, conform, analysis, bundle, _ := happyBundle(t)

	// The served bundle digest must match the record; a stale record digest is
	// an integrity failure even if every entry individually looks valid.
	staleConform := conform
	staleConform.BundleDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	if _, err := synctest.VerifyBundle(bundle, staleConform, analysis, fixture.Audio, fixture.Video); err == nil {
		t.Fatal("verifier accepted a conform record with a stale bundle digest")
	} else {
		t.Logf("correctly rejected stale digest: %v", err)
	}
}
