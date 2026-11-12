package storage

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"voice-timeline-renderer/internal/audiocodec"
	"voice-timeline-renderer/internal/domain"
	"voice-timeline-renderer/internal/testutil"
)

func sourceAssetFor(t *testing.T, id, name string, wav []byte) domain.SourceAsset {
	t.Helper()
	clip, info, err := audiocodec.DecodeWAV(wav)
	if err != nil {
		t.Fatalf("decode test payload: %v", err)
	}
	sum := sha256.Sum256(wav)
	measurements := audiocodec.Measure(clip.Samples)
	return domain.SourceAsset{
		ID:            id,
		Name:          name,
		Digest:        hex.EncodeToString(sum[:]),
		ByteCount:     info.ByteCount,
		SampleRate:    info.SampleRate,
		Channels:      info.Channels,
		BitsPerSample: info.BitsPerSample,
		FrameCount:    info.FrameCount,
		PeakDBFS:      measurements.PeakDBFS,
		RMSDBFS:       measurements.RMSDBFS,
	}
}

// Distinct recordings that share name, duration, format, and amplitude must
// each keep the exact bytes registered under their identifier. The old storage
// layer reused a peer's media based on those shared metadata fields.
func TestSaveSourceKeepsSameShapedRecordingsDistinct(t *testing.T) {
	repo, err := NewRepository(t.TempDir())
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	first := testutil.Tone(48000, 1, 4800, 440, 0.4, 0)
	second := testutil.Tone(48000, 1, 4800, 880, 0.4, 0.7)

	assetA := sourceAssetFor(t, "take_a", "narration", first)
	assetB := sourceAssetFor(t, "take_b", "narration", second)
	if assetA.ByteCount != assetB.ByteCount || assetA.FrameCount != assetB.FrameCount {
		t.Fatalf("test inputs should share shape: a=%+v b=%+v", assetA, assetB)
	}
	if assetA.Digest == assetB.Digest {
		t.Fatal("test inputs must differ in content")
	}

	if err := repo.SaveSource(assetA, first); err != nil {
		t.Fatalf("save A: %v", err)
	}
	if err := repo.SaveSource(assetB, second); err != nil {
		t.Fatalf("save B: %v", err)
	}

	for _, expected := range []struct {
		id  string
		wav []byte
	}{{"take_a", first}, {"take_b", second}} {
		asset, payload, err := repo.GetSource(expected.id)
		if err != nil {
			t.Fatalf("get %s: %v", expected.id, err)
		}
		if !bytes.Equal(payload, expected.wav) {
			t.Fatalf("%s media does not match the originally registered bytes", expected.id)
		}
		if asset.Digest != sourceAssetFor(t, expected.id, asset.Name, expected.wav).Digest {
			t.Fatalf("%s metadata digest does not describe stored media", expected.id)
		}
		onDisk, err := os.ReadFile(filepath.Join(repo.path("sources"), expected.id, "source.wav"))
		if err != nil {
			t.Fatalf("read %s media: %v", expected.id, err)
		}
		if !bytes.Equal(onDisk, expected.wav) {
			t.Fatalf("%s on-disk media was aliased to another recording", expected.id)
		}
	}
}

func TestSaveSourceRejectsMetadataDigestMismatch(t *testing.T) {
	repo, err := NewRepository(t.TempDir())
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	wav := testutil.Tone(48000, 1, 480, 440, 0.4, 0)
	asset := sourceAssetFor(t, "take_ok", "narration", wav)
	asset.Digest = "0000000000000000000000000000000000000000000000000000000000000000"
	if err := repo.SaveSource(asset, wav); err == nil {
		t.Fatal("expected digest mismatch to be rejected")
	}
	if _, err := os.Stat(filepath.Join(repo.path("sources"), "take_ok")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected source must leave no visible record, stat err=%v", err)
	}
}

func TestGetSourceDetectsTamperedMedia(t *testing.T) {
	repo, err := NewRepository(t.TempDir())
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	wav := testutil.Tone(48000, 1, 480, 440, 0.4, 0)
	asset := sourceAssetFor(t, "take_ok", "narration", wav)
	if err := repo.SaveSource(asset, wav); err != nil {
		t.Fatalf("save: %v", err)
	}
	tampered := append([]byte(nil), wav...)
	tampered[len(tampered)-1] ^= 0xFF
	if err := os.WriteFile(filepath.Join(repo.path("sources"), "take_ok", "source.wav"), tampered, 0o640); err != nil {
		t.Fatalf("tamper media: %v", err)
	}
	if _, _, err := repo.GetSource("take_ok"); err == nil {
		t.Fatal("expected integrity error for tampered media")
	} else if appErr := domain.AsAppError(err); appErr.Detail.Code != "source_integrity_error" {
		t.Fatalf("unexpected error code %q", appErr.Detail.Code)
	}
}
