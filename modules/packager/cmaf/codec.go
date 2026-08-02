package cmaf

import (
	"bytes"
	"fmt"

	"github.com/Eyevinn/mp4ff/aac"
	"github.com/Eyevinn/mp4ff/hevc"
	"github.com/Eyevinn/mp4ff/mp4"
)

func forceHLSVideoEntry(b *mp4.VisualSampleEntryBox) error {
	switch b.Type() {
	case "hvc1", "avc1":
		return nil
	case "hev1":
		if b.HvcC == nil {
			return unsupportedf(ReasonSampleEntry, "hev1 without hvcC")
		}
		if len(b.HvcC.GetNalusForType(hevc.NALU_VPS)) == 0 ||
			len(b.HvcC.GetNalusForType(hevc.NALU_SPS)) == 0 ||
			len(b.HvcC.GetNalusForType(hevc.NALU_PPS)) == 0 {
			return unsupportedf(ReasonInbandParamSets, "hev1 parameter sets are not in hvcC")
		}
		b.SetType("hvc1")
		return nil
	case "avc3":
		if b.AvcC == nil {
			return unsupportedf(ReasonSampleEntry, "avc3 without avcC")
		}
		if len(b.AvcC.SPSnalus) == 0 || len(b.AvcC.PPSnalus) == 0 {
			return unsupportedf(ReasonInbandParamSets, "avc3 parameter sets are not in avcC")
		}
		b.SetType("avc1")
		return nil
	default:
		return unsupportedf(ReasonSampleEntry, "video sample entry %s", b.Type())
	}
}

func videoCodecString(b *mp4.VisualSampleEntryBox) (string, error) {
	switch b.Type() {
	case "hvc1":
		spss := b.HvcC.GetNalusForType(hevc.NALU_SPS)
		if len(spss) == 0 {
			return "", unsupportedf(ReasonInbandParamSets, "hvc1 without sps in hvcC")
		}
		sps, err := hevc.ParseSPSNALUnit(spss[0])
		if err != nil {
			return "", fmt.Errorf("parse hevc sps: %w", err)
		}
		return hevc.CodecString("hvc1", sps), nil
	case "avc1":
		if b.AvcC == nil {
			return "", unsupportedf(ReasonSampleEntry, "avc1 without avcC")
		}
		return fmt.Sprintf("avc1.%02X%02X%02X",
			b.AvcC.AVCProfileIndication, b.AvcC.ProfileCompatibility, b.AvcC.AVCLevelIndication), nil
	default:
		return "", unsupportedf(ReasonCodec, "video codec %s", b.Type())
	}
}

func audioCodecString(b *mp4.AudioSampleEntryBox) (string, error) {
	if b.Type() != "mp4a" {
		return "", unsupportedf(ReasonCodec, "audio sample entry %s", b.Type())
	}
	if b.Esds == nil || b.Esds.DecConfigDescriptor == nil {
		return "", unsupportedf(ReasonSampleEntry, "mp4a without esds")
	}
	dc := b.Esds.DecConfigDescriptor
	if dc.DecSpecificInfo == nil || len(dc.DecSpecificInfo.DecConfig) == 0 {
		return "", unsupportedf(ReasonSampleEntry, "mp4a without audio specific config")
	}
	asc, err := aac.DecodeAudioSpecificConfig(bytes.NewReader(dc.DecSpecificInfo.DecConfig))
	if err != nil {
		return "", fmt.Errorf("decode audio specific config: %w", err)
	}
	return fmt.Sprintf("mp4a.%02X.%d", dc.ObjectType, asc.ObjectType), nil
}
