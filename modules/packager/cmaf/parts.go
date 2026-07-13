package cmaf

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"time"

	"github.com/Eyevinn/mp4ff/mp4"
)

// RewritePartSequence updates the mfhd sequence number of one staged CMAF
// fragment without decoding or copying its media payload.
func RewritePartSequence(file io.ReadWriteSeeker, sequence uint32) error {
	if file == nil {
		return unsupportedf(ReasonMalformed, "nil part file")
	}
	end, err := file.Seek(0, io.SeekEnd)
	if err != nil || end <= 0 {
		return unsupportedf(ReasonMalformed, "read part size: %v", err)
	}
	for offset := int64(0); offset < end; {
		typ, payload, boxEnd, boxErr := readPartBox(file, offset, end)
		if boxErr != nil {
			return boxErr
		}
		if typ == "moof" {
			for child := payload; child < boxEnd; {
				childType, childPayload, childEnd, childErr := readPartBox(file, child, boxEnd)
				if childErr != nil {
					return childErr
				}
				if childType == "mfhd" {
					if childEnd-childPayload < 8 {
						return unsupportedf(ReasonMalformed, "mfhd payload is truncated")
					}
					if _, err := file.Seek(childPayload+4, io.SeekStart); err != nil {
						return fmt.Errorf("seek mfhd sequence: %w", err)
					}
					var encoded [4]byte
					binary.BigEndian.PutUint32(encoded[:], sequence)
					if _, err := file.Write(encoded[:]); err != nil {
						return fmt.Errorf("write mfhd sequence: %w", err)
					}
					return nil
				}
				child = childEnd
			}
		}
		offset = boxEnd
	}
	return unsupportedf(ReasonMalformed, "part has no mfhd box")
}

func readPartBox(file io.ReadSeeker, offset, limit int64) (string, int64, int64, error) {
	if limit-offset < 8 {
		return "", 0, 0, unsupportedf(ReasonMalformed, "box header is truncated")
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return "", 0, 0, fmt.Errorf("seek box header: %w", err)
	}
	var header [16]byte
	if _, err := io.ReadFull(file, header[:8]); err != nil {
		return "", 0, 0, unsupportedf(ReasonMalformed, "read box header: %v", err)
	}
	size := uint64(binary.BigEndian.Uint32(header[:4]))
	headerSize := int64(8)
	switch size {
	case 1:
		if limit-offset < 16 {
			return "", 0, 0, unsupportedf(ReasonMalformed, "extended box header is truncated")
		}
		if _, err := io.ReadFull(file, header[8:16]); err != nil {
			return "", 0, 0, unsupportedf(ReasonMalformed, "read extended box header: %v", err)
		}
		size = binary.BigEndian.Uint64(header[8:16])
		headerSize = 16
	case 0:
		size = uint64(limit - offset)
	}
	if size < uint64(headerSize) || size > uint64(limit-offset) {
		return "", 0, 0, unsupportedf(ReasonMalformed, "invalid box size %d", size)
	}
	return string(header[4:8]), offset + headerSize, offset + int64(size), nil
}

// Part is one independently parseable fMP4 fragment. BaseTime and Duration
// use the track timescale from Init.
type Part struct {
	Data        []byte
	BaseTime    uint64
	Duration    uint64
	Independent bool
	Events      []EventMessage
}

type queuedPartSample struct {
	sample mp4.FullSample
	events []EventMessage
}

// SplitParts divides a clear single-track CMAF media segment at sample
// boundaries. A positive target is required; the final part may be shorter.
func (i *Init) SplitParts(clearSegment []byte, target time.Duration) (parts []Part, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			parts = nil
			err = unsupportedf(ReasonMalformed, "part split crashed the box parser: %v", recovered)
		}
	}()
	return i.splitParts(clearSegment, target, nil)
}

// SplitPartsFromSequence divides a clear single-track CMAF media segment and
// assigns consecutive mfhd sequence numbers beginning at firstSequence.
func (i *Init) SplitPartsFromSequence(
	clearSegment []byte,
	target time.Duration,
	firstSequence uint32,
) (parts []Part, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			parts = nil
			err = unsupportedf(ReasonMalformed, "part split crashed the box parser: %v", recovered)
		}
	}()
	return i.splitParts(clearSegment, target, &firstSequence)
}

func (i *Init) splitParts(clearSegment []byte, target time.Duration, requestedSequence *uint32) ([]Part, error) {
	if i == nil {
		return nil, unsupportedf(ReasonMalformed, "nil init segment")
	}
	if target <= 0 {
		return nil, unsupportedf(ReasonMalformed, "part target must be positive")
	}
	if i.Track.Timescale == 0 {
		return nil, unsupportedf(ReasonMalformed, "track timescale is zero")
	}
	targetTicks, err := durationTicks(target, i.Track.Timescale)
	if err != nil {
		return nil, err
	}

	file, err := decodeOwnedSegment(bytes.Clone(clearSegment))
	if err != nil {
		return nil, unsupportedf(ReasonMalformed, "decode clear media segment: %v", err)
	}
	if len(file.Segments) != 1 {
		return nil, unsupportedf(ReasonNotFragmented, "expected 1 media segment, got %d", len(file.Segments))
	}
	samples, sourceSequence, err := i.partSamples(file.Segments[0])
	if err != nil {
		return nil, err
	}

	parts := make([]Part, 0, len(samples))
	current := make([]mp4.FullSample, 0)
	var currentEvents []EventMessage
	var currentDuration uint64
	sequence := sourceSequence
	if requestedSequence != nil {
		sequence = *requestedSequence
	}
	sequenceExhausted := false
	flush := func() error {
		if sequenceExhausted {
			return unsupportedf(ReasonMalformed, "part sequence overflows uint32")
		}
		part, err := i.encodePart(sequence, current, currentEvents, currentDuration)
		if err != nil {
			return err
		}
		parts = append(parts, part)
		if sequence == math.MaxUint32 {
			sequenceExhausted = true
		} else {
			sequence++
		}
		current = current[:0]
		currentEvents = nil
		currentDuration = 0
		return nil
	}

	for _, item := range samples {
		current = append(current, item.sample)
		currentEvents = append(currentEvents, item.events...)
		if currentDuration > math.MaxUint64-uint64(item.sample.Dur) {
			return nil, unsupportedf(ReasonMalformed, "part duration overflows")
		}
		currentDuration += uint64(item.sample.Dur)
		if currentDuration >= targetTicks {
			if err := flush(); err != nil {
				return nil, err
			}
		}
	}
	if len(current) > 0 {
		if err := flush(); err != nil {
			return nil, err
		}
	}
	return parts, nil
}

func durationTicks(target time.Duration, timescale uint32) (uint64, error) {
	seconds := uint64(target / time.Second)
	remainder := uint64(target % time.Second)
	remainderTicks := remainder * uint64(timescale) / uint64(time.Second)
	if seconds > (math.MaxUint64-remainderTicks)/uint64(timescale) {
		return 0, unsupportedf(ReasonMalformed, "part target overflows track timescale")
	}
	ticks := seconds*uint64(timescale) + remainderTicks
	if ticks == 0 {
		ticks = 1
	}
	return ticks, nil
}

func (i *Init) partSamples(segment *mp4.MediaSegment) ([]queuedPartSample, uint32, error) {
	if segment == nil || len(segment.Fragments) == 0 {
		return nil, 0, unsupportedf(ReasonNotFragmented, "media segment has no fragment")
	}
	trex := trackTrex(i.di, i.Track.ID)
	items := make([]queuedPartSample, 0)
	fragmentEvents := eventsByFragment(segment)
	var firstSequence uint32
	var expectedDecodeTime uint64
	haveExpectedDecodeTime := false

	for fragmentIndex, fragment := range segment.Fragments {
		if fragment == nil || fragment.Moof == nil || fragment.Moof.Mfhd == nil || fragment.Mdat == nil {
			return nil, 0, unsupportedf(ReasonNotFragmented, "fragment %d is incomplete", fragmentIndex)
		}
		if fragmentIndex == 0 {
			firstSequence = fragment.Moof.Mfhd.SequenceNumber
		}
		if len(fragment.Moof.Trafs) != 1 || fragment.Moof.Trafs[0].Tfhd == nil ||
			fragment.Moof.Trafs[0].Tfhd.TrackID != i.Track.ID {
			return nil, 0, unsupportedf(ReasonNotFragmented, "fragment %d is not single-track track %d", fragmentIndex, i.Track.ID)
		}
		if fragment.Moof.Trafs[0].Tfdt == nil {
			return nil, 0, unsupportedf(ReasonNotFragmented, "fragment %d has no tfdt", fragmentIndex)
		}
		fragmentSamples, err := fragment.GetFullSamples(trex)
		if err != nil {
			return nil, 0, unsupportedf(ReasonMalformed, "read fragment %d samples: %v", fragmentIndex, err)
		}
		if len(fragmentSamples) == 0 {
			return nil, 0, unsupportedf(ReasonNotFragmented, "fragment %d has no samples", fragmentIndex)
		}
		for sampleIndex, sample := range fragmentSamples {
			if sample.Dur == 0 {
				return nil, 0, unsupportedf(ReasonMalformed, "fragment %d sample %d has zero duration", fragmentIndex, sampleIndex)
			}
			if uint64(sample.Size) != uint64(len(sample.Data)) {
				return nil, 0, unsupportedf(ReasonMalformed, "fragment %d sample %d data size mismatch", fragmentIndex, sampleIndex)
			}
			if haveExpectedDecodeTime && sample.DecodeTime != expectedDecodeTime {
				return nil, 0, unsupportedf(
					ReasonMalformed,
					"fragment %d sample %d decode time %d, want %d",
					fragmentIndex,
					sampleIndex,
					sample.DecodeTime,
					expectedDecodeTime,
				)
			}
			if sample.DecodeTime > math.MaxUint64-uint64(sample.Dur) {
				return nil, 0, unsupportedf(ReasonMalformed, "fragment %d sample %d decode time overflows", fragmentIndex, sampleIndex)
			}
			expectedDecodeTime = sample.DecodeTime + uint64(sample.Dur)
			haveExpectedDecodeTime = true
			item := queuedPartSample{sample: sample}
			item.sample.Data = bytes.Clone(sample.Data)
			if sampleIndex == 0 {
				item.events = fragmentEvents[fragmentIndex]
			}
			items = append(items, item)
		}
	}
	return items, firstSequence, nil
}

func (i *Init) encodePart(
	sequence uint32,
	samples []mp4.FullSample,
	events []EventMessage,
	duration uint64,
) (Part, error) {
	if len(samples) == 0 {
		return Part{}, unsupportedf(ReasonMalformed, "cannot encode an empty part")
	}
	fragment, err := mp4.CreateFragment(sequence, i.Track.ID)
	if err != nil {
		return Part{}, fmt.Errorf("create part fragment: %w", err)
	}
	for _, event := range events {
		fragment.AddEmsg(eventBox(event))
	}
	for _, sample := range samples {
		if err := fragment.AddFullSampleToTrack(sample, i.Track.ID); err != nil {
			return Part{}, fmt.Errorf("add part sample: %w", err)
		}
	}
	if fragment.Size() > uint64(maxInt()) {
		return Part{}, unsupportedf(ReasonMalformed, "encoded part is too large")
	}
	buffer := bytes.NewBuffer(make([]byte, 0, int(fragment.Size())))
	if err := fragment.Encode(buffer); err != nil {
		return Part{}, fmt.Errorf("encode part fragment: %w", err)
	}
	independent := i.Track.Kind != KindVideo || samples[0].IsSync()
	return Part{
		Data:        buffer.Bytes(),
		BaseTime:    samples[0].DecodeTime,
		Duration:    duration,
		Independent: independent,
		Events:      cloneEvents(events),
	}, nil
}

func eventsByFragment(segment *mp4.MediaSegment) [][]EventMessage {
	grouped := make([][]EventMessage, len(segment.Fragments))
	for fragmentIndex, fragment := range segment.Fragments {
		seenMdat := false
		for _, child := range fragment.Children {
			switch box := child.(type) {
			case *mp4.MdatBox:
				seenMdat = true
			case *mp4.EmsgBox:
				target := fragmentIndex
				// mp4ff associates an emsg between fragments with the preceding
				// fragment. Its box order still shows that it precedes the next moof.
				if seenMdat && fragmentIndex+1 < len(segment.Fragments) {
					target++
				}
				grouped[target] = append(grouped[target], eventMessage(box))
			}
		}
	}
	return grouped
}

func eventBox(event EventMessage) *mp4.EmsgBox {
	return &mp4.EmsgBox{
		Version:               event.Version,
		TimeScale:             event.Timescale,
		PresentationTimeDelta: event.PresentationTimeDelta,
		PresentationTime:      event.PresentationTime,
		EventDuration:         event.EventDuration,
		ID:                    event.ID,
		SchemeIDURI:           event.SchemeIDURI,
		Value:                 event.Value,
		MessageData:           bytes.Clone(event.MessageData),
	}
}

func cloneEvents(events []EventMessage) []EventMessage {
	if len(events) == 0 {
		return nil
	}
	cloned := make([]EventMessage, len(events))
	copy(cloned, events)
	for index := range cloned {
		cloned[index].MessageData = bytes.Clone(events[index].MessageData)
	}
	return cloned
}

func maxInt() int {
	return int(^uint(0) >> 1)
}
