// Package cmaf turns encrypted DASH CMAF tracks into clear HLS-compatible fMP4.
package cmaf

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/Eyevinn/mp4ff/bits"
	"github.com/Eyevinn/mp4ff/mp4"
)

type Kind string

const (
	KindVideo Kind = "video"
	KindAudio Kind = "audio"
	KindText  Kind = "text"
)

var supportedSchemes = map[string]struct{}{"cenc": {}, "cbcs": {}}

type KeySet map[string][]byte

func NewKeySet(pairs map[string]string) (KeySet, error) {
	out := make(KeySet, len(pairs))
	for kid, key := range pairs {
		nk := NormalizeKID(kid)
		if len(nk) != 32 {
			return nil, fmt.Errorf("kid %q is not 16 bytes", kid)
		}
		raw, err := hex.DecodeString(NormalizeKID(key))
		if err != nil {
			return nil, fmt.Errorf("key for kid %s: %w", nk, err)
		}
		if len(raw) != 16 {
			return nil, fmt.Errorf("key for kid %s is %d bytes, want 16", nk, len(raw))
		}
		out[nk] = raw
	}
	return out, nil
}

func NormalizeKID(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "0x")
	return strings.ReplaceAll(s, "-", "")
}

type Track struct {
	ID         uint32
	Kind       Kind
	Codec      string
	Timescale  uint32
	Width      uint16
	Height     uint16
	SampleRate int
	Channels   uint16
	KID        string
	Encrypted  bool
	Scheme     string
}

type Init struct {
	Clear []byte
	Track Track

	di mp4.DecryptInfo
}

// ParseInit reads bytes an upstream gave us, so a defect in the box parser must
// not be able to take the process down with it.
func ParseInit(raw []byte) (init *Init, err error) {
	defer func() {
		if r := recover(); r != nil {
			init, err = nil, unsupportedf(ReasonMalformed, "init segment crashed the box parser: %v", r)
		}
	}()
	return parseInit(raw)
}

func parseInit(raw []byte) (*Init, error) {
	f, err := mp4.DecodeFile(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("decode init segment: %w", err)
	}
	if f.Init == nil || f.Init.Moov == nil {
		return nil, unsupportedf(ReasonNotFragmented, "no moov in init segment")
	}
	moov := f.Init.Moov
	if moov.Mvex == nil {
		return nil, unsupportedf(ReasonNotFragmented, "no mvex in init segment")
	}
	switch len(moov.Traks) {
	case 1:
	case 0:
		return nil, unsupportedf(ReasonNoTrack, "init segment has no trak")
	default:
		return nil, unsupportedf(ReasonMultiTrackInit, "init segment has %d traks", len(moov.Traks))
	}

	trak := moov.Traks[0]
	if trak.Tkhd == nil || trak.Mdia == nil || trak.Mdia.Mdhd == nil ||
		trak.Mdia.Minf == nil || trak.Mdia.Minf.Stbl == nil || trak.Mdia.Minf.Stbl.Stsd == nil {
		return nil, unsupportedf(ReasonNotFragmented, "incomplete trak")
	}

	di, err := mp4.DecryptInit(f.Init)
	if err != nil {
		return nil, unsupportedf(ReasonScheme, "%v", err)
	}
	if err := prepareTextProtection(trak.Mdia.Minf.Stbl.Stsd, trak.Tkhd.TrackID, &di); err != nil {
		return nil, err
	}

	track := Track{ID: trak.Tkhd.TrackID}
	track.Timescale = trak.Mdia.Mdhd.Timescale

	for _, ti := range di.TrackInfos {
		if ti.TrackID != track.ID || ti.Sinf == nil {
			continue
		}
		scheme := ti.Sinf.Schm.SchemeType
		if _, ok := supportedSchemes[scheme]; !ok {
			return nil, unsupportedf(ReasonScheme, "scheme %s", scheme)
		}
		if ti.Sinf.Schi == nil || ti.Sinf.Schi.Tenc == nil {
			return nil, unsupportedf(ReasonMissingKID, "no tenc for track %d", track.ID)
		}
		kid := ti.Sinf.Schi.Tenc.DefaultKID
		if len(kid) != 16 {
			return nil, unsupportedf(ReasonMissingKID, "tenc kid is %d bytes", len(kid))
		}
		track.Encrypted = true
		track.Scheme = scheme
		track.KID = hex.EncodeToString(kid)
	}

	if err := describeSampleEntry(trak.Mdia.Minf.Stbl.Stsd, &track); err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if err := f.Init.Encode(&buf); err != nil {
		return nil, fmt.Errorf("encode clear init: %w", err)
	}
	return &Init{Clear: buf.Bytes(), Track: track, di: di}, nil
}

func prepareTextProtection(stsd *mp4.StsdBox, trackID uint32, di *mp4.DecryptInfo) error {
	if stsd.Stpp == nil {
		return nil
	}
	var protection *mp4.SinfBox
	kept := make([]mp4.Box, 0, len(stsd.Stpp.Children))
	for _, child := range stsd.Stpp.Children {
		if sinf, ok := child.(*mp4.SinfBox); ok {
			if protection != nil {
				return unsupportedf(ReasonMultiSampleEntry, "stpp carries more than one sinf")
			}
			protection = sinf
			continue
		}
		kept = append(kept, child)
	}
	if protection == nil {
		return nil
	}
	if protection.Frma == nil || protection.Frma.DataFormat != "stpp" {
		return unsupportedf(ReasonSampleEntry, "stpp protection has no stpp original format")
	}
	if protection.Schm == nil {
		return unsupportedf(ReasonScheme, "stpp protection has no schm")
	}
	if _, ok := supportedSchemes[protection.Schm.SchemeType]; !ok {
		return unsupportedf(ReasonScheme, "scheme %s", protection.Schm.SchemeType)
	}
	if protection.Schi == nil || protection.Schi.Tenc == nil {
		return unsupportedf(ReasonMissingKID, "no tenc for text track %d", trackID)
	}

	found := false
	for index := range di.TrackInfos {
		if di.TrackInfos[index].TrackID == trackID {
			di.TrackInfos[index].Sinf = protection
			found = true
			break
		}
	}
	if !found {
		return unsupportedf(ReasonNotFragmented, "no decrypt info for text track %d", trackID)
	}
	stsd.Stpp.Children = kept
	return nil
}

func describeSampleEntry(stsd *mp4.StsdBox, track *Track) error {
	var video *mp4.VisualSampleEntryBox
	var audio *mp4.AudioSampleEntryBox
	var text *mp4.StppBox
	for _, child := range stsd.Children {
		switch entry := child.(type) {
		case *mp4.VisualSampleEntryBox:
			if video != nil || audio != nil || text != nil {
				return unsupportedf(ReasonMultiSampleEntry, "more than one sample entry")
			}
			video = entry
		case *mp4.AudioSampleEntryBox:
			if video != nil || audio != nil || text != nil {
				return unsupportedf(ReasonMultiSampleEntry, "more than one sample entry")
			}
			audio = entry
		case *mp4.StppBox:
			if video != nil || audio != nil || text != nil {
				return unsupportedf(ReasonMultiSampleEntry, "more than one sample entry")
			}
			text = entry
		default:
			return unsupportedf(ReasonSampleEntry, "sample entry %s", child.Type())
		}
	}

	switch {
	case video != nil:
		if err := forceHLSVideoEntry(video); err != nil {
			return err
		}
		codec, err := videoCodecString(video)
		if err != nil {
			return err
		}
		track.Kind = KindVideo
		track.Codec = codec
		track.Width = video.Width
		track.Height = video.Height
	case audio != nil:
		codec, err := audioCodecString(audio)
		if err != nil {
			return err
		}
		track.Kind = KindAudio
		track.Codec = codec
		track.Channels = audio.ChannelCount
		track.SampleRate = int(audio.SampleRate)
	case text != nil:
		track.Kind = KindText
		track.Codec = "stpp"
	default:
		return unsupportedf(ReasonNoTrack, "stsd has no sample entry")
	}
	return nil
}

type Segment struct {
	Clear    []byte
	BaseTime uint64
	Duration uint64
	Events   []EventMessage
}

func (i *Init) Decrypt(raw []byte, keys KeySet) (*Segment, error) {
	return i.decryptOwnedReserved(bytes.Clone(raw), keys, nil)
}

func (i *Init) DecryptOwnedReserved(raw []byte, keys KeySet, reserve func(int64) error) (*Segment, error) {
	return i.decryptOwnedReserved(raw, keys, reserve)
}

func (i *Init) decryptOwnedReserved(raw []byte, keys KeySet, reserve func(int64) error) (seg *Segment, err error) {
	defer func() {
		if r := recover(); r != nil {
			seg, err = nil, unsupportedf(ReasonMalformed, "media segment crashed the box parser: %v", r)
		}
	}()
	return i.decryptChecked(raw, keys, reserve)
}

func (i *Init) decryptChecked(raw []byte, keys KeySet, reserve func(int64) error) (*Segment, error) {
	seg, err := i.decodeMediaSegment(raw, keys)
	if err != nil {
		return nil, err
	}

	base, dur, err := segmentTiming(seg, i.di, i.Track.ID)
	if err != nil {
		return nil, err
	}

	size := int64(encodedSegmentSize(seg))
	if reserve != nil {
		if err := reserve(size); err != nil {
			return nil, err
		}
	}
	maxInt := int64(^uint(0) >> 1)
	if size < 0 || size > maxInt {
		return nil, unsupportedf(ReasonMalformed, "clear segment size %d does not fit in memory", size)
	}
	clear := make([]byte, int(size))
	w := bits.NewFixedSliceWriterFromSlice(clear)
	if err := seg.EncodeSW(w); err != nil {
		return nil, fmt.Errorf("encode clear segment: %w", err)
	}
	if err := w.AccError(); err != nil {
		return nil, fmt.Errorf("encode clear segment: %w", err)
	}
	return &Segment{
		Clear:    w.Bytes(),
		BaseTime: base,
		Duration: dur,
		Events:   eventMessages(seg),
	}, nil
}

func encodedSegmentSize(seg *mp4.MediaSegment) uint64 {
	var size uint64
	if seg.Styp != nil {
		size += seg.Styp.Size()
	}
	for _, sidx := range seg.Sidxs {
		size += sidx.Size()
	}
	for _, fragment := range seg.Fragments {
		size += fragment.Size()
	}
	return size
}

func (i *Init) decodeMediaSegment(raw []byte, keys KeySet) (*mp4.MediaSegment, error) {
	f, err := decodeOwnedSegment(raw)
	if err != nil {
		return nil, fmt.Errorf("decode media segment: %w", err)
	}
	if len(f.Segments) != 1 {
		return nil, unsupportedf(ReasonNotFragmented, "expected 1 media segment, got %d", len(f.Segments))
	}
	seg := f.Segments[0]
	if len(seg.Fragments) == 0 {
		return nil, unsupportedf(ReasonNotFragmented, "media segment has no fragment")
	}

	if i.Track.Encrypted {
		key, ok := keys[i.Track.KID]
		if !ok {
			return nil, unsupportedf(ReasonMissingKey, "no key for kid %s", i.Track.KID)
		}
		err = decryptProtectedWithKey(seg, i.di, key, i.Track.Scheme)
		if err != nil {
			return nil, fmt.Errorf("decrypt segment (kid %s): %w", i.Track.KID, err)
		}
	}

	return seg, nil
}

func decodeOwnedSegment(raw []byte) (*mp4.File, error) {
	f, err := mp4.DecodeFile(bytes.NewReader(raw), mp4.WithDecodeMode(mp4.DecModeLazyMdat))
	if err != nil {
		return nil, err
	}
	for _, segment := range f.Segments {
		for _, fragment := range segment.Fragments {
			mdat := fragment.Mdat
			if mdat == nil {
				return nil, errors.New("fragment has no mdat")
			}
			start := mdat.PayloadAbsoluteOffset()
			size := mdat.GetLazyDataSize()
			if start > uint64(len(raw)) || size > uint64(len(raw))-start {
				return nil, errors.New("mdat payload is outside the segment")
			}
			mdat.SetData(raw[start : start+size : start+size])
		}
	}
	return f, nil
}

func segmentTiming(seg *mp4.MediaSegment, di mp4.DecryptInfo, trackID uint32) (uint64, uint64, error) {
	var base uint64
	var total uint64
	found := false
	for _, frag := range seg.Fragments {
		for _, traf := range frag.Moof.Trafs {
			if traf.Tfhd.TrackID != trackID {
				continue
			}
			if traf.Tfdt == nil {
				return 0, 0, unsupportedf(ReasonNotFragmented, "traf without tfdt")
			}
			if !found {
				base = traf.Tfdt.BaseMediaDecodeTime()
				found = true
			}
			var trex *mp4.TrexBox
			for _, ti := range di.TrackInfos {
				if ti.TrackID == trackID {
					trex = ti.Trex
					break
				}
			}
			samples, err := frag.GetFullSamples(trex)
			if err != nil {
				return 0, 0, fmt.Errorf("read samples: %w", err)
			}
			for _, s := range samples {
				total += uint64(s.Dur)
			}
		}
	}
	if !found {
		return 0, 0, unsupportedf(ReasonNotFragmented, "no traf for track %d", trackID)
	}
	return base, total, nil
}
