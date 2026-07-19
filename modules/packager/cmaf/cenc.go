package cmaf

import (
	"crypto/aes"
	"crypto/cipher"
	"fmt"

	"github.com/Eyevinn/mp4ff/mp4"
)

// decryptProtectedWithKey mirrors mp4ff's strict segment rewrite while
// reusing one AES block across all samples in the segment.
func decryptProtectedWithKey(seg *mp4.MediaSegment, di mp4.DecryptInfo, key []byte, scheme string) error {
	for _, frag := range seg.Fragments {
		for _, traf := range frag.Moof.Trafs {
			hasSenc, _ := traf.ContainsSencBox()
			if hasSenc && decryptTrackInfo(di, traf.Tfhd.TrackID).Sinf == nil {
				return fmt.Errorf("no decrypt info for trackID=%d which has senc box", traf.Tfhd.TrackID)
			}
		}
	}

	var block cipher.Block
	for _, frag := range seg.Fragments {
		var removed uint64
		for _, traf := range frag.Moof.Trafs {
			ti := decryptTrackInfo(di, traf.Tfhd.TrackID)
			if ti.Sinf == nil {
				continue
			}
			if ti.Sinf.Schm.SchemeType != scheme {
				return fmt.Errorf("scheme type %s not supported", ti.Sinf.Schm.SchemeType)
			}
			tenc := ti.Sinf.Schi.Tenc
			hasSenc, parsed := traf.ContainsSencBox()
			if !hasSenc && (tenc == nil || tenc.DefaultPerSampleIVSize != 0 || len(tenc.DefaultConstantIV) == 0) {
				return fmt.Errorf("no senc box in traf")
			}
			if hasSenc && !parsed {
				if err := traf.ParseReadSenc(tenc.DefaultPerSampleIVSize, frag.Moof.StartPos); err != nil {
					return fmt.Errorf("parseReadSenc: %w", err)
				}
			}

			samples, err := frag.GetFullSamples(ti.Trex)
			if err != nil {
				return err
			}
			var senc *mp4.SencBox
			switch {
			case traf.Senc != nil:
				senc = traf.Senc
			case traf.UUIDSenc != nil:
				senc = traf.UUIDSenc.Senc
			}
			switch scheme {
			case "cenc":
				if len(samples) > 0 {
					if err := ensureAESBlock(&block, key); err != nil {
						return err
					}
				}
				err = decryptCENCSamples(block, samples, tenc, senc)
			case "cbcs":
				err = decryptCBCSSamples(&block, key, samples, tenc, senc)
			default:
				err = fmt.Errorf("scheme type %s not supported", scheme)
			}
			if err != nil {
				return err
			}
			removed += traf.RemoveEncryptionBoxes()
		}

		_, psshBytes := frag.Moof.RemovePsshs()
		removed += psshBytes
		for _, traf := range frag.Moof.Trafs {
			for _, trun := range traf.Truns {
				trun.DataOffset -= int32(removed)
			}
		}
		if frag.Mdat.StartPos > frag.Moof.StartPos {
			frag.Mdat.StartPos -= removed
		}
	}
	if len(seg.Sidxs) > 0 {
		seg.Sidx = nil
		seg.Sidxs = nil
	}
	return nil
}

func ensureAESBlock(block *cipher.Block, key []byte) error {
	if *block != nil {
		return nil
	}
	created, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	*block = created
	return nil
}

func decryptTrackInfo(di mp4.DecryptInfo, trackID uint32) mp4.DecryptTrackInfo {
	for _, ti := range di.TrackInfos {
		if ti.TrackID == trackID {
			return ti
		}
	}
	return mp4.DecryptTrackInfo{}
}

func decryptCENCSamples(block cipher.Block, samples []mp4.FullSample, tenc *mp4.TencBox, senc *mp4.SencBox) error {
	var iv [aes.BlockSize]byte
	if tenc.DefaultConstantIV != nil {
		copy(iv[:], tenc.DefaultConstantIV)
	}
	for index := range samples {
		if senc != nil && len(senc.IVs) == len(samples) {
			if len(senc.IVs[index]) < len(iv) {
				clear(iv[:])
			}
			copy(iv[:], senc.IVs[index])
		}
		var patterns []mp4.SubSamplePattern
		if senc != nil && len(senc.SubSamples) != 0 {
			patterns = senc.SubSamples[index]
		}
		stream := cencStream{block: block, counter: iv, used: aes.BlockSize}
		if len(patterns) == 0 {
			stream.xor(samples[index].Data)
			continue
		}
		var offset uint32
		for _, pattern := range patterns {
			offset += uint32(pattern.BytesOfClearData)
			protected := pattern.BytesOfProtectedData
			if protected > 0 {
				stream.xor(samples[index].Data[offset : offset+protected])
				offset += protected
			}
		}
	}
	return nil
}

type cencStream struct {
	block     cipher.Block
	counter   [aes.BlockSize]byte
	keystream [aes.BlockSize]byte
	used      int
}

func (s *cencStream) xor(data []byte) {
	for len(data) > 0 {
		if s.used == len(s.keystream) {
			s.block.Encrypt(s.keystream[:], s.counter[:])
			incrementCENCCounter(&s.counter)
			s.used = 0
		}
		count := min(len(data), len(s.keystream)-s.used)
		for index := range count {
			data[index] ^= s.keystream[s.used+index]
		}
		data = data[count:]
		s.used += count
	}
}

func incrementCENCCounter(counter *[aes.BlockSize]byte) {
	for index := len(counter) - 1; index >= 0; index-- {
		counter[index]++
		if counter[index] != 0 {
			return
		}
	}
}

func decryptCBCSSamples(
	block *cipher.Block,
	key []byte,
	samples []mp4.FullSample,
	tenc *mp4.TencBox,
	senc *mp4.SencBox,
) error {
	var iv [aes.BlockSize]byte
	if tenc.DefaultConstantIV != nil {
		copy(iv[:], tenc.DefaultConstantIV)
	}
	for index := range samples {
		if senc != nil && len(senc.IVs) == len(samples) {
			if len(senc.IVs[index]) < len(iv) {
				clear(iv[:])
			}
			copy(iv[:], senc.IVs[index])
		}
		var patterns []mp4.SubSamplePattern
		if senc != nil && len(senc.SubSamples) != 0 {
			patterns = senc.SubSamples[index]
		}
		if len(patterns) == 0 {
			if err := ensureAESBlock(block, key); err != nil {
				return err
			}
			if err := decryptCBCSRange(*block, samples[index].Data, iv, tenc); err != nil {
				return err
			}
			continue
		}
		var offset uint32
		for _, pattern := range patterns {
			offset += uint32(pattern.BytesOfClearData)
			protected := pattern.BytesOfProtectedData
			if protected > 0 {
				if err := ensureAESBlock(block, key); err != nil {
					return err
				}
				if err := decryptCBCSRange(*block, samples[index].Data[offset:offset+protected], iv, tenc); err != nil {
					return err
				}
			}
			offset += protected
		}
	}
	return nil
}

func decryptCBCSRange(block cipher.Block, data []byte, iv [aes.BlockSize]byte, tenc *mp4.TencBox) error {
	cryptBytes := int(tenc.DefaultCryptByteBlock) * aes.BlockSize
	skipBytes := int(tenc.DefaultSkipByteBlock) * aes.BlockSize
	if skipBytes == 0 {
		decryptCBCBlocks(block, data[:len(data)&^(aes.BlockSize-1)], &iv)
		return nil
	}
	if cryptBytes == 0 {
		return fmt.Errorf("cbcs crypt block is zero while skip block is non-zero")
	}
	for offset := 0; len(data)-offset >= cryptBytes; {
		decryptCBCBlocks(block, data[offset:offset+cryptBytes], &iv)
		offset += cryptBytes
		if len(data)-offset < skipBytes {
			break
		}
		offset += skipBytes
	}
	return nil
}

func decryptCBCBlocks(block cipher.Block, data []byte, iv *[aes.BlockSize]byte) {
	var ciphertext [aes.BlockSize]byte
	for offset := 0; offset < len(data); offset += aes.BlockSize {
		current := data[offset : offset+aes.BlockSize]
		copy(ciphertext[:], current)
		block.Decrypt(current, current)
		for index := range current {
			current[index] ^= iv[index]
		}
		*iv = ciphertext
	}
}
