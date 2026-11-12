package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"testing"

	"voice-timeline-renderer/internal/domain"
	"voice-timeline-renderer/internal/storage"
	"voice-timeline-renderer/internal/testutil"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	repo, err := storage.NewRepository(t.TempDir())
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	return New(repo, DefaultConfig())
}

func writeSource(t *testing.T, svc *Service, id, name string, wav []byte) (domain.SourceAsset, bool, error) {
	t.Helper()
	return svc.CreateSource(context.Background(), id, SourceWriteRequest{
		Name:      name,
		WAVBase64: base64.StdEncoding.EncodeToString(wav),
	})
}

func digestOf(wav []byte) string {
	sum := sha256.Sum256(wav)
	return hex.EncodeToString(sum[:])
}

func TestSameShapedDistinctRecordingsKeepTheirOwnIdentity(t *testing.T) {
	svc := newTestService(t)
	first := testutil.Tone(48000, 1, 4800, 440, 0.4, 0)
	second := testutil.Tone(48000, 1, 4800, 880, 0.4, 0.7)

	assetA, created, err := writeSource(t, svc, "take_a", "narration", first)
	if err != nil || !created {
		t.Fatalf("register A: created=%v err=%v", created, err)
	}
	assetB, created, err := writeSource(t, svc, "take_b", "narration", second)
	if err != nil || !created {
		t.Fatalf("register B: created=%v err=%v", created, err)
	}
	if assetA.Digest != digestOf(first) {
		t.Fatalf("A digest does not describe A: got %s want %s", assetA.Digest, digestOf(first))
	}
	if assetB.Digest != digestOf(second) {
		t.Fatalf("B digest does not describe B: got %s want %s", assetB.Digest, digestOf(second))
	}
	if assetA.Digest == assetB.Digest {
		t.Fatalf("distinct recordings collapsed to one digest %s", assetA.Digest)
	}

	readA, payloadA, err := svc.repo.GetSource("take_a")
	if err != nil {
		t.Fatalf("read A: %v", err)
	}
	if readA.Digest != digestOf(first) || digestOf(payloadA) != digestOf(first) {
		t.Fatalf("A was rebound after B registration: metadata=%s media=%s", readA.Digest, digestOf(payloadA))
	}
	readB, payloadB, err := svc.repo.GetSource("take_b")
	if err != nil {
		t.Fatalf("read B: %v", err)
	}
	if readB.Digest != digestOf(second) || digestOf(payloadB) != digestOf(second) {
		t.Fatalf("B media/metadata disagree: metadata=%s media=%s", readB.Digest, digestOf(payloadB))
	}
}

func TestRegistrationIsIdempotentOnlyForIdenticalContent(t *testing.T) {
	svc := newTestService(t)
	original := testutil.Tone(48000, 1, 4800, 440, 0.4, 0)
	if _, created, err := writeSource(t, svc, "take_a", "narration", original); err != nil || !created {
		t.Fatalf("initial register: created=%v err=%v", created, err)
	}

	replay, created, err := writeSource(t, svc, "take_a", "narration", original)
	if err != nil || created {
		t.Fatalf("identical replay: created=%v err=%v", created, err)
	}
	if replay.Digest != digestOf(original) {
		t.Fatalf("identical replay digest mismatch: got %s", replay.Digest)
	}

	// A different recording under the same ID conflicts, even though it shares
	// name, duration, format, and amplitude with the original.
	replacement := testutil.Tone(48000, 1, 4800, 880, 0.4, 0.3)
	if _, _, err := writeSource(t, svc, "take_a", "narration", replacement); err == nil {
		t.Fatal("expected different content under an existing ID to conflict")
	} else if appErr := domain.AsAppError(err); appErr.Detail.Code != "source_conflict" {
		t.Fatalf("unexpected error code %q", appErr.Detail.Code)
	}

	stored, payload, err := svc.repo.GetSource("take_a")
	if err != nil {
		t.Fatalf("read after conflict: %v", err)
	}
	if stored.Digest != digestOf(original) || digestOf(payload) != digestOf(original) {
		t.Fatalf("conflicting write altered the original binding: metadata=%s", stored.Digest)
	}
}

func TestSameRecordingMayBeRegisteredUnderAnotherIdentifier(t *testing.T) {
	svc := newTestService(t)
	recording := testutil.Tone(48000, 1, 4800, 440, 0.4, 0)
	if _, created, err := writeSource(t, svc, "take_a", "narration", recording); err != nil || !created {
		t.Fatalf("register A: created=%v err=%v", created, err)
	}
	copyAsset, created, err := writeSource(t, svc, "take_b", "narration copy", recording)
	if err != nil || !created {
		t.Fatalf("register A copy: created=%v err=%v", created, err)
	}
	if copyAsset.Digest != digestOf(recording) {
		t.Fatalf("copy digest mismatch: got %s", copyAsset.Digest)
	}
}
