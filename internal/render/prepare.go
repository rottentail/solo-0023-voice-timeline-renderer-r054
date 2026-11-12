package render

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"

	"voice-timeline-renderer/internal/audiocodec"
	"voice-timeline-renderer/internal/domain"
)

type SourceInput struct {
	Asset domain.SourceAsset
	WAV   []byte
}

var sourceClipPool = struct {
	sync.Mutex
	clips map[string]*audiocodec.Clip
	order []string
}{clips: make(map[string]*audiocodec.Clip)}

// loadSourceClip caches decoded clips by content digest. An asset's digest
// commits to the exact media bytes, so equal digests imply equal samples;
// metadata such as name, frame count, or format is shared by many distinct
// recordings and must never be used to identify a clip.
func loadSourceClip(key string, wav []byte) (*audiocodec.Clip, error) {
	sourceClipPool.Lock()
	if cached, ok := sourceClipPool.clips[key]; ok {
		result := &audiocodec.Clip{
			SampleRate: cached.SampleRate,
			Channels:   cached.Channels,
			Samples:    append([]float32(nil), cached.Samples...),
		}
		sourceClipPool.Unlock()
		return result, nil
	}
	sourceClipPool.Unlock()
	clip, _, err := audiocodec.DecodeWAV(wav)
	if err != nil {
		return nil, err
	}
	sourceClipPool.Lock()
	defer sourceClipPool.Unlock()
	if _, exists := sourceClipPool.clips[key]; !exists {
		if len(sourceClipPool.order) >= 16 {
			oldest := sourceClipPool.order[0]
			delete(sourceClipPool.clips, oldest)
			sourceClipPool.order = sourceClipPool.order[1:]
		}
		sourceClipPool.clips[key] = &audiocodec.Clip{
			SampleRate: clip.SampleRate,
			Channels:   clip.Channels,
			Samples:    append([]float32(nil), clip.Samples...),
		}
		sourceClipPool.order = append(sourceClipPool.order, key)
	}
	return clip, nil
}

type preparedSpan struct {
	Index            int
	SourceID         string
	SourceDigest     string
	SourceStartFrame int64
	SourceEndFrame   int64
	OutputStartFrame int64
	OutputEndFrame   int64
	OutputFrames     int64
	CrossfadeFrames  int64
	GainDB           float64
	ChannelAction    string
	Clip             *audiocodec.Clip
}

func prepareSpans(
	ctx context.Context,
	plan domain.Timeline,
	sources map[string]SourceInput,
) ([]preparedSpan, int64, error) {
	type resolvedSource struct {
		clip   *audiocodec.Clip
		digest string
	}
	decoded := make(map[string]resolvedSource, len(sources))
	prepared := make([]preparedSpan, 0, len(plan.Spans))
	cursor := int64(0)
	for index, span := range plan.Spans {
		source, ok := sources[span.SourceID]
		if !ok {
			return nil, 0, errors.New("timeline references a missing source")
		}
		resolved, ok := decoded[span.SourceID]
		if !ok {
			// Bind the receipt to the bytes that are actually mixed. A recorded
			// digest is a commitment to the stored media; if it contradicts the
			// supplied bytes the source is corrupt and must not be rendered under
			// the claimed identity. An absent digest (older records) is accepted
			// and the receipt uses the measured digest instead.
			actual := sha256.Sum256(source.WAV)
			actualDigest := hex.EncodeToString(actual[:])
			if source.Asset.Digest != "" && actualDigest != source.Asset.Digest {
				return nil, 0, domain.E(
					domain.KindInternal,
					"source_integrity_error",
					"source_id",
					"stored source media does not match its registered digest",
				)
			}
			parsed, err := loadSourceClip(actualDigest, source.WAV)
			if err != nil {
				return nil, 0, err
			}
			resolved = resolvedSource{clip: parsed, digest: actualDigest}
			decoded[span.SourceID] = resolved
		}
		clip := resolved.clip
		selected, err := audiocodec.SliceFrames(ctx, clip, span.SourceStartFrame, span.SourceEndFrame)
		if err != nil {
			return nil, 0, err
		}
		resampled, err := audiocodec.Resample(ctx, selected, plan.SampleRate)
		if err != nil {
			return nil, 0, err
		}
		mapped, action, err := audiocodec.MapChannels(ctx, resampled, plan.OutputChannels)
		if err != nil {
			return nil, 0, err
		}
		frames := mapped.FrameCount()
		if frames < 1 {
			return nil, 0, errors.New("span produced no output frames")
		}

		start := cursor
		if index > 0 {
			start -= span.CrossfadeFrames
		}
		if start < 0 {
			return nil, 0, errors.New("crossfade produced a negative output position")
		}
		end := start + frames
		cursor = end

		prepared = append(prepared, preparedSpan{
			Index:            index,
			SourceID:         span.SourceID,
			SourceDigest:     resolved.digest,
			SourceStartFrame: span.SourceStartFrame,
			SourceEndFrame:   span.SourceEndFrame,
			OutputStartFrame: start,
			OutputEndFrame:   end,
			OutputFrames:     frames,
			CrossfadeFrames:  span.CrossfadeFrames,
			GainDB:           span.GainDB,
			ChannelAction:    action,
			Clip:             mapped,
		})
	}
	return prepared, cursor, nil
}
