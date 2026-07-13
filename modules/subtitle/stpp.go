package subtitle

import (
	"bytes"
	"fmt"
	"math"
	"time"

	"github.com/Eyevinn/mp4ff/mp4"
)

// DecodeSTPPSamples reads TTML samples from a fragmented ISO BMFF media
// segment. Returned payloads are owned by the caller and do not alias raw.
func DecodeSTPPSamples(raw []byte, track STPPTrack) (samples []STPPSample, err error) {
	if track.ID == 0 {
		return nil, fmt.Errorf("stpp track ID must be non-zero")
	}
	if track.Timescale == 0 {
		return nil, fmt.Errorf("stpp track timescale must be non-zero")
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			samples = nil
			err = fmt.Errorf("decode stpp media segment: invalid sample boundary: %v", recovered)
		}
	}()

	file, err := mp4.DecodeFile(bytes.NewReader(raw), mp4.WithDecodeMode(mp4.DecModeLazyMdat))
	if err != nil {
		return nil, fmt.Errorf("decode stpp media segment: %w", err)
	}
	if len(file.Segments) == 0 {
		return nil, fmt.Errorf("decode stpp media segment: no media segment")
	}
	trex := mp4.CreateTrex(track.ID)
	trex.DefaultSampleDuration = track.DefaultSampleDuration
	trex.DefaultSampleSize = track.DefaultSampleSize
	trex.DefaultSampleFlags = track.DefaultSampleFlags

	for _, segment := range file.Segments {
		for _, fragment := range segment.Fragments {
			if err := loadMdat(raw, fragment); err != nil {
				return nil, err
			}
			fullSamples, err := fragment.GetFullSamples(trex)
			if err != nil {
				return nil, fmt.Errorf("read stpp samples: %w", err)
			}
			for _, sample := range fullSamples {
				decoded, err := decodeSTPPSample(sample, track.Timescale)
				if err != nil {
					return nil, err
				}
				samples = append(samples, decoded)
			}
		}
	}
	return samples, nil
}

// ParseSTPPSample parses standard stpp timing, where TTML times are relative to
// the track origin and the sample presentation interval is a clipping window.
func ParseSTPPSample(sample STPPSample, timing TimingParameters) ([]Cue, error) {
	if sample.End <= sample.Start {
		return nil, fmt.Errorf("invalid stpp sample window [%v, %v)", sample.Start, sample.End)
	}
	cues, err := ParseTTML(sample.Payload, TTMLParseOptions{
		DefaultDuration: sample.End,
		Timing:          timing,
	})
	if err != nil {
		return nil, err
	}
	return ClipCues(cues, sample.Start, sample.End), nil
}

func loadMdat(raw []byte, fragment *mp4.Fragment) error {
	if fragment == nil || fragment.Mdat == nil {
		return fmt.Errorf("decode stpp media segment: fragment has no mdat")
	}
	start := fragment.Mdat.PayloadAbsoluteOffset()
	size := fragment.Mdat.GetLazyDataSize()
	if start > uint64(len(raw)) || size > uint64(len(raw))-start {
		return fmt.Errorf("decode stpp media segment: mdat payload is outside the segment")
	}
	fragment.Mdat.SetData(raw[start : start+size : start+size])
	return nil
}

func decodeSTPPSample(sample mp4.FullSample, timescale uint32) (STPPSample, error) {
	if sample.DecodeTime > math.MaxInt64 {
		return STPPSample{}, fmt.Errorf("stpp decode time %d overflows duration", sample.DecodeTime)
	}
	decodeTicks := int64(sample.DecodeTime)
	offset := int64(sample.CompositionTimeOffset)
	if offset > 0 && decodeTicks > math.MaxInt64-offset {
		return STPPSample{}, fmt.Errorf("stpp presentation time overflows duration")
	}
	presentationTicks := decodeTicks + offset
	decodeTime, err := ticksToDuration(decodeTicks, timescale)
	if err != nil {
		return STPPSample{}, fmt.Errorf("convert stpp decode time: %w", err)
	}
	start, err := ticksToDuration(presentationTicks, timescale)
	if err != nil {
		return STPPSample{}, fmt.Errorf("convert stpp presentation time: %w", err)
	}
	duration, err := ticksToDuration(int64(sample.Dur), timescale)
	if err != nil {
		return STPPSample{}, fmt.Errorf("convert stpp sample duration: %w", err)
	}
	if duration > 0 && start > time.Duration(math.MaxInt64)-duration {
		return STPPSample{}, fmt.Errorf("stpp sample end overflows duration")
	}
	return STPPSample{
		DecodeTime: decodeTime,
		Start:      start,
		End:        start + duration,
		Duration:   duration,
		Payload:    bytes.Clone(sample.Data),
	}, nil
}

func ticksToDuration(ticks int64, timescale uint32) (time.Duration, error) {
	scale := int64(timescale)
	wholeSeconds := ticks / scale
	remainder := ticks % scale
	maxSeconds := int64(math.MaxInt64) / int64(time.Second)
	minSeconds := int64(math.MinInt64) / int64(time.Second)
	if wholeSeconds > maxSeconds || wholeSeconds < minSeconds {
		return 0, fmt.Errorf("%d ticks at timescale %d overflow duration", ticks, timescale)
	}
	wholeNanoseconds := wholeSeconds * int64(time.Second)
	fractionNanoseconds := remainder * int64(time.Second) / scale
	if fractionNanoseconds > 0 && wholeNanoseconds > math.MaxInt64-fractionNanoseconds {
		return 0, fmt.Errorf("%d ticks at timescale %d overflow duration", ticks, timescale)
	}
	if fractionNanoseconds < 0 && wholeNanoseconds < math.MinInt64-fractionNanoseconds {
		return 0, fmt.Errorf("%d ticks at timescale %d underflow duration", ticks, timescale)
	}
	nanoseconds := wholeNanoseconds + fractionNanoseconds
	return time.Duration(nanoseconds), nil
}
