// Package cmaf turns encrypted DASH CMAF tracks into clear HLS-compatible fMP4
// without decoding, re-encoding or touching compressed samples.
package cmaf

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/Eyevinn/mp4ff/mp4"
)

type Kind string

const (
	KindVideo Kind = "video"
	KindAudio Kind = "audio"
)

// supportedSchemes is the whitelist. cenc is AES-CTR full-sample encryption;
// cbcs is AES-CBC pattern encryption. cens and cbc1 stay out: no fixture, so no
// claim.
var supportedSchemes = map[string]struct{}{"cenc": {}, "cbcs": {}}

// KeySet maps a normalized KID to its 16-byte content key.
type KeySet map[string][]byte

// NewKeySet parses hex kid:key pairs. Dashes and 0x prefixes are tolerated.
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

// Init is a parsed, decrypted, HLS-ready init segment plus everything needed to
// decrypt the media segments that reference it.
type Init struct {
	Clear []byte
	Track Track

	di mp4.DecryptInfo
}

// ParseInit strips CENC protection from a DASH init segment and normalizes its
// sample entry for HLS. The KID comes from the track's tenc box, never the MPD.
func ParseInit(raw []byte) (*Init, error) {
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

	di, err := mp4.DecryptInit(f.Init)
	if err != nil {
		return nil, unsupportedf(ReasonScheme, "%v", err)
	}

	trak := moov.Traks[0]
	track := Track{ID: trak.Tkhd.TrackID}
	if trak.Mdia == nil || trak.Mdia.Mdhd == nil || trak.Mdia.Minf == nil || trak.Mdia.Minf.Stbl == nil {
		return nil, unsupportedf(ReasonNotFragmented, "incomplete trak")
	}
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

// describeSampleEntry reads the entry left behind by DecryptInit. The stsd
// typed pointers still point at encv/enca, so walk Children instead.
func describeSampleEntry(stsd *mp4.StsdBox, track *Track) error {
	var video *mp4.VisualSampleEntryBox
	var audio *mp4.AudioSampleEntryBox
	for _, child := range stsd.Children {
		switch entry := child.(type) {
		case *mp4.VisualSampleEntryBox:
			if video != nil || audio != nil {
				return unsupportedf(ReasonMultiSampleEntry, "more than one sample entry")
			}
			video = entry
		case *mp4.AudioSampleEntryBox:
			if video != nil || audio != nil {
				return unsupportedf(ReasonMultiSampleEntry, "more than one sample entry")
			}
			audio = entry
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
	default:
		return unsupportedf(ReasonNoTrack, "stsd has no sample entry")
	}
	return nil
}

// Segment is a decrypted media segment ready to publish.
type Segment struct {
	Clear    []byte
	BaseTime uint64
	Duration uint64
}

// Decrypt decrypts one media segment in place and re-encodes it. Keys are
// matched strictly by the track KID; a missing key is an error, never a
// silent fallback to some other key.
func (i *Init) Decrypt(raw []byte, keys KeySet) (*Segment, error) {
	f, err := mp4.DecodeFile(bytes.NewReader(raw))
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
		if _, ok := keys[i.Track.KID]; !ok {
			return nil, unsupportedf(ReasonMissingKey, "no key for kid %s", i.Track.KID)
		}
		if err := mp4.DecryptSegmentWithKeys(seg, i.di, nil, keys, true); err != nil {
			return nil, fmt.Errorf("decrypt segment (kid %s): %w", i.Track.KID, err)
		}
	}

	base, dur, err := segmentTiming(seg, i.di, i.Track.ID)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if err := seg.Encode(&buf); err != nil {
		return nil, fmt.Errorf("encode clear segment: %w", err)
	}
	return &Segment{Clear: buf.Bytes(), BaseTime: base, Duration: dur}, nil
}

// segmentTiming derives the decode time and duration from the fragments
// themselves so the playlist never has to trust the manifest's arithmetic.
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
