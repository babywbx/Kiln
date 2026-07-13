package cmaf

import (
	"bytes"
	"fmt"
	"math"

	"github.com/Eyevinn/mp4ff/mp4"
)

// TextSample is one decoded stpp sample in the track timescale.
type TextSample struct {
	DecodeTime       uint64
	PresentationTime int64
	Duration         uint32
	Payload          []byte
}

// TextSegment contains clear stpp samples and aggregate fragment timing.
type TextSegment struct {
	Timescale uint32
	BaseTime  uint64
	Duration  uint64
	Samples   []TextSample
	Events    []EventMessage
}

// DecodeText decrypts, when necessary, and extracts stpp samples from a media
// segment without mutating or retaining the caller-owned input.
func (i *Init) DecodeText(raw []byte, keys KeySet) (decoded *TextSegment, err error) {
	if i.Track.Kind != KindText {
		return nil, unsupportedf(ReasonHandler, "track kind %s is not text", i.Track.Kind)
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			decoded = nil
			err = unsupportedf(ReasonMalformed, "text segment crashed the box parser: %v", recovered)
		}
	}()

	segment, err := i.decodeMediaSegment(bytes.Clone(raw), keys)
	if err != nil {
		return nil, err
	}
	base, duration, err := segmentTiming(segment, i.di, i.Track.ID)
	if err != nil {
		return nil, err
	}
	trex := trackTrex(i.di, i.Track.ID)
	result := &TextSegment{
		Timescale: i.Track.Timescale,
		BaseTime:  base,
		Duration:  duration,
		Events:    eventMessages(segment),
	}
	for _, fragment := range segment.Fragments {
		samples, err := fragment.GetFullSamples(trex)
		if err != nil {
			return nil, fmt.Errorf("read text samples: %w", err)
		}
		for _, sample := range samples {
			if sample.DecodeTime > math.MaxInt64 {
				return nil, unsupportedf(ReasonMalformed, "text sample decode time overflows presentation time")
			}
			decodeTime := int64(sample.DecodeTime)
			offset := int64(sample.CompositionTimeOffset)
			if offset > 0 && decodeTime > math.MaxInt64-offset {
				return nil, unsupportedf(ReasonMalformed, "text sample presentation time overflows")
			}
			result.Samples = append(result.Samples, TextSample{
				DecodeTime:       sample.DecodeTime,
				PresentationTime: decodeTime + offset,
				Duration:         sample.Dur,
				Payload:          bytes.Clone(sample.Data),
			})
		}
	}
	return result, nil
}

func trackTrex(di mp4.DecryptInfo, trackID uint32) *mp4.TrexBox {
	for _, info := range di.TrackInfos {
		if info.TrackID == trackID {
			return info.Trex
		}
	}
	return nil
}
