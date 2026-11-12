package render

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"voice-timeline-renderer/internal/audiocodec"
	"voice-timeline-renderer/internal/domain"
	"voice-timeline-renderer/internal/testutil"
)

func sourceInput(id, name string, wav []byte) SourceInput {
	sum := sha256.Sum256(wav)
	return SourceInput{
		Asset: domain.SourceAsset{
			ID:         id,
			Name:       name,
			Digest:     hex.EncodeToString(sum[:]),
			FrameCount: 4800,
			SampleRate: 48000,
			Channels:   1,
		},
		WAV: wav,
	}
}

func renderSpan(t *testing.T, sources map[string]SourceInput, spans []domain.TimelineSpan) Result {
	t.Helper()
	plan := domain.Timeline{
		ID:             "timeline",
		Name:           "plan",
		SampleRate:     48000,
		OutputChannels: 1,
		TargetPeakDBFS: -1,
		Spans:          spans,
	}
	result, err := Render(context.Background(), "render", plan, sources, Limits{MaxOutputFrames: 48000 * 180}, time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return result
}

// The clip cache must key on content, not on shared metadata. Two recordings
// with the same name, format, and length previously collapsed to whichever was
// decoded first, silently duplicating one line in the mix.
func TestCacheDoesNotCollapseSameShapedRecordings(t *testing.T) {
	first := testutil.Tone(48000, 1, 4800, 440, 0.4, 0)
	second := testutil.Tone(48000, 1, 4800, 880, 0.4, 0.9)
	result := renderSpan(t, map[string]SourceInput{
		"take_a": sourceInput("take_a", "narration", first),
		"take_b": sourceInput("take_b", "narration", second),
	}, []domain.TimelineSpan{
		{SourceID: "take_a", SourceStartFrame: 0, SourceEndFrame: 1000, GainDB: 0},
		{SourceID: "take_b", SourceStartFrame: 0, SourceEndFrame: 1000, GainDB: 0},
	})
	if len(result.Receipt.Spans) != 2 {
		t.Fatalf("receipt span count = %d", len(result.Receipt.Spans))
	}
	firstClip, _, _ := testutil.DecodeSamples(first)
	secondClip, _, _ := testutil.DecodeSamples(second)
	gain := audiocodec.DBToLinear(result.Receipt.Normalization.AppliedGainDB)
	decoded, _, err := testutil.DecodeSamples(result.WAV)
	if err != nil {
		t.Fatalf("decode output: %v", err)
	}
	for frame := 0; frame < 1000; frame += 31 {
		if diff := float64(decoded[frame]) - float64(firstClip[frame])*gain; diff < -0.02 || diff > 0.02 {
			t.Fatalf("first segment frame %d diverged from take_a", frame)
		}
		if diff := float64(decoded[1000+frame]) - float64(secondClip[frame])*gain; diff < -0.02 || diff > 0.02 {
			t.Fatalf("second segment frame %d diverged from take_b", frame)
		}
	}
	identical := true
	for frame := 0; frame < 1000; frame++ {
		if decoded[frame] != decoded[1000+frame] {
			identical = false
			break
		}
	}
	if identical {
		t.Fatal("output halves are identical")
	}
}

// The receipt must report the digest of the bytes that were mixed and refuse to
// render media whose bytes contradict the recorded digest.
func TestReceiptBindsMeasuredDigestAndRejectsContradiction(t *testing.T) {
	recording := testutil.Tone(48000, 1, 4800, 440, 0.4, 0)
	other := testutil.Tone(48000, 1, 4800, 880, 0.4, 0)
	source := sourceInput("take_a", "narration", recording)
	result := renderSpan(t, map[string]SourceInput{"take_a": source}, []domain.TimelineSpan{
		{SourceID: "take_a", SourceStartFrame: 0, SourceEndFrame: 1000, GainDB: 0},
	})
	if result.Receipt.Spans[0].SourceDigest != source.Asset.Digest {
		t.Fatalf("receipt digest = %s, want %s", result.Receipt.Spans[0].SourceDigest, source.Asset.Digest)
	}

	// Contradictory metadata must fail rather than render under a false identity.
	mismatched := sourceInput("take_a", "narration", other)
	mismatched.Asset.Digest = source.Asset.Digest
	plan := domain.Timeline{
		ID: "timeline", Name: "plan", SampleRate: 48000, OutputChannels: 1, TargetPeakDBFS: -1,
		Spans: []domain.TimelineSpan{{SourceID: "take_a", SourceStartFrame: 0, SourceEndFrame: 1000}},
	}
	if _, err := Render(context.Background(), "render", plan, map[string]SourceInput{"take_a": mismatched}, Limits{}, time.Unix(0, 0).UTC()); err == nil {
		t.Fatal("expected render to reject contradictory source digest")
	}

	// A legacy record without a digest still renders and reports measured digest.
	legacy := sourceInput("take_a", "narration", recording)
	legacy.Asset.Digest = ""
	result = renderSpan(t, map[string]SourceInput{"take_a": legacy}, []domain.TimelineSpan{
		{SourceID: "take_a", SourceStartFrame: 0, SourceEndFrame: 1000, GainDB: 0},
	})
	if result.Receipt.Spans[0].SourceDigest != digestOfWAV(recording) {
		t.Fatalf("legacy receipt digest = %s", result.Receipt.Spans[0].SourceDigest)
	}
}

func digestOfWAV(wav []byte) string {
	sum := sha256.Sum256(wav)
	return hex.EncodeToString(sum[:])
}
