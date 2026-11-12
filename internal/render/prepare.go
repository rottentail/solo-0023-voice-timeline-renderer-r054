package render

import (
	"context"
	"errors"
	"fmt"
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

func loadSourceClip(source SourceInput) (*audiocodec.Clip, error) {
	key := fmt.Sprintf("%s/%d/%d/%d/%d", source.Asset.Name,
		source.Asset.SampleRate, source.Asset.Channels,
		source.Asset.BitsPerSample, source.Asset.FrameCount)
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
	clip, _, err := audiocodec.DecodeWAV(source.WAV)
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
	decoded := make(map[string]*audiocodec.Clip, len(sources))
	prepared := make([]preparedSpan, 0, len(plan.Spans))
	cursor := int64(0)
	for index, span := range plan.Spans {
		source, ok := sources[span.SourceID]
		if !ok {
			return nil, 0, errors.New("timeline references a missing source")
		}
		clip, ok := decoded[span.SourceID]
		if !ok {
			parsed, err := loadSourceClip(source)
			if err != nil {
				return nil, 0, err
			}
			clip = parsed
			decoded[span.SourceID] = parsed
		}
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
			SourceDigest:     source.Asset.Digest,
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
