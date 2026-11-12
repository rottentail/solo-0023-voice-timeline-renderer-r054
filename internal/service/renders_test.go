package service

import (
	"context"
	"math"
	"testing"

	"voice-timeline-renderer/internal/audiocodec"
	"voice-timeline-renderer/internal/domain"
	"voice-timeline-renderer/internal/storage"
	"voice-timeline-renderer/internal/testutil"
)

func makeTimeline(svc *Service, id string, spans []domain.TimelineSpan) (domain.Timeline, error) {
	plan, _, err := svc.CreateTimeline(context.Background(), id, TimelineWriteRequest{
		Name:           "same named takes",
		SampleRate:     48000,
		OutputChannels: 1,
		TargetPeakDBFS: -1,
		Spans:          spans,
	})
	return plan, err
}

func renderJob(svc *Service, id, timelineID string) (domain.RenderJob, error) {
	job, _, err := svc.CreateRender(context.Background(), id, RenderWriteRequest{TimelineID: timelineID})
	return job, err
}

// Same-named, equal-length recordings placed in one timeline must contribute
// their own selected intervals, and the receipt must report the digest of each.
func TestRenderKeepsSameNamedRecordingsDistinct(t *testing.T) {
	svc := newTestService(t)
	first := testutil.Tone(48000, 1, 4800, 440, 0.4, 0)
	second := testutil.Tone(48000, 1, 4800, 880, 0.4, 0.9)
	third := testutil.Tone(48000, 1, 4800, 660, 0.4, 1.5)

	for _, item := range []struct {
		id  string
		wav []byte
	}{{"take_a", first}, {"take_b", second}, {"take_c", third}} {
		if _, created, err := writeSource(t, svc, item.id, "narration", item.wav); err != nil || !created {
			t.Fatalf("register %s: created=%v err=%v", item.id, created, err)
		}
	}

	spans := []domain.TimelineSpan{
		{SourceID: "take_a", SourceStartFrame: 0, SourceEndFrame: 2000, GainDB: 0},
		{SourceID: "take_b", SourceStartFrame: 1000, SourceEndFrame: 3000, GainDB: 0},
		{SourceID: "take_c", SourceStartFrame: 2000, SourceEndFrame: 4800, GainDB: -1},
	}
	plan, err := makeTimeline(svc, "same_names", spans)
	if err != nil {
		t.Fatalf("compose timeline: %v", err)
	}
	job, err := renderJob(svc, "mix_one", plan.ID)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if job.State != domain.RenderCompleted || job.Output == nil {
		t.Fatalf("render did not complete: %+v", job)
	}
	receipt, err := svc.GetReceipt(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("receipt: %v", err)
	}
	if len(receipt.Spans) != 3 {
		t.Fatalf("receipt span count = %d", len(receipt.Spans))
	}
	wantDigests := []string{digestOf(first), digestOf(second), digestOf(third)}
	for index, want := range wantDigests {
		if receipt.Spans[index].SourceDigest != want {
			t.Fatalf("span %d digest = %s, want %s", index, receipt.Spans[index].SourceDigest, want)
		}
	}
	if receipt.Spans[1].OutputStartFrame != 2000 || receipt.Spans[2].OutputStartFrame != 4000 {
		t.Fatalf("spans not placed consecutively: %+v", receipt.Spans)
	}

	out, _, err := svc.GetOutput(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("output: %v", err)
	}
	if digestOf(out) != receipt.Output.Digest {
		t.Fatalf("published output digest does not match receipt")
	}
	clip, _, err := audiocodec.DecodeWAV(out)
	if err != nil {
		t.Fatalf("decode output: %v", err)
	}
	gain := audiocodec.DBToLinear(receipt.Normalization.AppliedGainDB)
	check := func(name string, outStart int, source []float32, sourceStart int, frames int, gainDB float64) {
		for frame := 0; frame < frames; frame += 53 {
			want := float64(source[sourceStart+frame]) * audiocodec.DBToLinear(gainDB) * gain
			got := float64(clip.Samples[outStart+frame])
			if math.Abs(want-got) > 0.02 {
				t.Fatalf("%s output frame %d = %.6f, want %.6f", name, outStart+frame, got, want)
			}
		}
	}
	firstClip, _, _ := audiocodec.DecodeWAV(first)
	secondClip, _, _ := audiocodec.DecodeWAV(second)
	thirdClip, _, _ := audiocodec.DecodeWAV(third)
	check("take_a", 0, firstClip.Samples, 0, 2000, 0)
	check("take_b", 2000, secondClip.Samples, 1000, 2000, 0)
	check("take_c", 4000, thirdClip.Samples, 2000, 800, -1)

	identical := true
	for frame := 0; frame < 2000; frame++ {
		if clip.Samples[frame] != clip.Samples[2000+frame] {
			identical = false
			break
		}
	}
	if identical {
		t.Fatal("same-named recordings produced identical output segments")
	}
}

// Reopening the same data directory must reproduce byte-identical output and
// keep every span bound to its registered recording.
func TestRenderIsStableAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	repo, err := storage.NewRepository(dir)
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	svc := New(repo, DefaultConfig())
	first := testutil.Tone(48000, 1, 4800, 440, 0.4, 0)
	second := testutil.Tone(48000, 1, 4800, 880, 0.4, 0.9)
	if _, created, err := writeSource(t, svc, "take_a", "narration", first); err != nil || !created {
		t.Fatalf("register A: created=%v err=%v", created, err)
	}
	if _, created, err := writeSource(t, svc, "take_b", "narration", second); err != nil || !created {
		t.Fatalf("register B: created=%v err=%v", created, err)
	}
	plan, err := makeTimeline(svc, "same_names", []domain.TimelineSpan{
		{SourceID: "take_a", SourceStartFrame: 0, SourceEndFrame: 2000, GainDB: 0},
		{SourceID: "take_b", SourceStartFrame: 0, SourceEndFrame: 2000, GainDB: 0},
	})
	if err != nil {
		t.Fatalf("compose timeline: %v", err)
	}
	jobOne, err := renderJob(svc, "mix_one", plan.ID)
	if err != nil {
		t.Fatalf("first render: %v", err)
	}

	reopened, err := storage.NewRepository(dir)
	if err != nil {
		t.Fatalf("reopen repository: %v", err)
	}
	svcTwo := New(reopened, DefaultConfig())
	jobTwo, err := renderJob(svcTwo, "mix_two", plan.ID)
	if err != nil {
		t.Fatalf("second render: %v", err)
	}
	if jobOne.Output == nil || jobTwo.Output == nil {
		t.Fatalf("missing output metadata: %+v / %+v", jobOne.Output, jobTwo.Output)
	}
	if jobOne.Output.Digest != jobTwo.Output.Digest {
		t.Fatalf("render changed across restart: %s vs %s", jobOne.Output.Digest, jobTwo.Output.Digest)
	}
	receiptTwo, err := svcTwo.GetReceipt(context.Background(), jobTwo.ID)
	if err != nil {
		t.Fatalf("receipt after restart: %v", err)
	}
	if receiptTwo.Spans[1].SourceDigest != digestOf(second) {
		t.Fatalf("post-restart receipt span 1 digest = %s", receiptTwo.Spans[1].SourceDigest)
	}
}
