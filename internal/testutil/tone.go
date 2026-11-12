package testutil

import (
	"math"

	"voice-timeline-renderer/internal/audiocodec"
)

// Tone builds a deterministic 16-bit PCM WAV. Distinct phase/frequency inputs
// yield recordings that share duration, format, and amplitude but not samples.
func Tone(sampleRate, channels, frames int, frequency, amplitude, phase float64) []byte {
	result := make([]float32, frames*channels)
	for frame := 0; frame < frames; frame++ {
		seconds := float64(frame) / float64(sampleRate)
		value := float32(math.Sin(2*math.Pi*frequency*seconds+phase) * amplitude)
		if channels == 1 {
			result[frame] = value
			continue
		}
		result[frame*2] = value
		result[frame*2+1] = value * 0.5
	}
	wav, err := audiocodec.EncodePCM16(sampleRate, channels, result)
	if err != nil {
		panic(err)
	}
	return wav
}

// DecodeSamples returns the decoded sample stream and frame count of a WAV.
func DecodeSamples(wav []byte) ([]float32, int64, error) {
	clip, info, err := audiocodec.DecodeWAV(wav)
	if err != nil {
		return nil, 0, err
	}
	return clip.Samples, info.FrameCount, nil
}
