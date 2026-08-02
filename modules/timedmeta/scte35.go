package timedmeta

import (
	"errors"
	"fmt"
)

type Command uint8

const (
	CommandSpliceInsert Command = 0x05
	CommandTimeSignal   Command = 0x06
)

type SCTE35 struct {
	Command            Command
	EventID            uint32
	Direction          Direction
	Cancelled          bool
	AutoReturn         bool
	SplicePTS          *uint64
	BreakDuration90k   *uint64
	SegmentationTypeID *uint8
}

const (
	scteTableID = 0xfc
	ptsMask     = uint64(1<<33) - 1
)

func ParseSCTE35(payload []byte) (SCTE35, error) {
	if len(payload) < 3 {
		return SCTE35{}, errors.New("SCTE-35 section is shorter than its header")
	}
	if payload[0] != scteTableID {
		return SCTE35{}, fmt.Errorf("SCTE-35 table_id is 0x%02x, want 0xfc", payload[0])
	}
	sectionLength := int(payload[1]&0x0f)<<8 | int(payload[2])
	total := 3 + sectionLength
	if sectionLength < 17 || total != len(payload) {
		return SCTE35{}, fmt.Errorf("SCTE-35 section_length %d does not match payload of %d bytes", sectionLength, len(payload))
	}
	if payload[1]&0xc0 != 0 {
		return SCTE35{}, errors.New("SCTE-35 section syntax and private flags must be zero")
	}
	if payload[3] != 0 {
		return SCTE35{}, fmt.Errorf("unsupported SCTE-35 protocol_version %d", payload[3])
	}
	section := payload[:total]
	if mpegCRC32(section) != 0 {
		return SCTE35{}, errors.New("SCTE-35 CRC_32 mismatch")
	}

	r := bitReader{data: section[:len(section)-4]}
	if _, err := r.read(24); err != nil {
		return SCTE35{}, err
	}
	if _, err := r.read(8); err != nil { // protocol_version
		return SCTE35{}, err
	}
	encrypted, err := r.read(1)
	if err != nil {
		return SCTE35{}, err
	}
	if _, err = r.read(6); err != nil { // encryption_algorithm
		return SCTE35{}, err
	}
	ptsAdjustment, err := r.read(33)
	if err != nil {
		return SCTE35{}, err
	}
	if encrypted != 0 {
		return SCTE35{}, errors.New("encrypted SCTE-35 sections are unsupported")
	}
	if _, err = r.read(8); err != nil { // cw_index
		return SCTE35{}, err
	}
	if _, err = r.read(12); err != nil { // tier
		return SCTE35{}, err
	}
	commandLength, err := r.read(12)
	if err != nil {
		return SCTE35{}, err
	}
	commandType, err := r.read(8)
	if err != nil {
		return SCTE35{}, err
	}
	if commandLength == 0xfff {
		return SCTE35{}, errors.New("SCTE-35 splice_command_length 0xfff is unsupported")
	}
	command, err := r.readBytes(int(commandLength))
	if err != nil {
		return SCTE35{}, fmt.Errorf("SCTE-35 command: %w", err)
	}
	descriptorLength, err := r.read(16)
	if err != nil {
		return SCTE35{}, fmt.Errorf("SCTE-35 descriptor loop: %w", err)
	}
	descriptors, err := r.readBytes(int(descriptorLength))
	if err != nil {
		return SCTE35{}, fmt.Errorf("SCTE-35 descriptor loop: %w", err)
	}
	if r.remainingBits() != 0 {
		return SCTE35{}, fmt.Errorf("SCTE-35 has %d bytes between descriptors and CRC", r.remainingBits()/8)
	}

	info := SCTE35{Command: Command(commandType), Direction: DirectionUnknown}
	switch info.Command {
	case CommandSpliceInsert:
		if err := parseSpliceInsert(command, ptsAdjustment, &info); err != nil {
			return SCTE35{}, err
		}
	case CommandTimeSignal:
		if err := parseTimeSignal(command, ptsAdjustment, &info); err != nil {
			return SCTE35{}, err
		}
	}
	if err := parseDescriptors(descriptors, &info); err != nil {
		return SCTE35{}, err
	}
	return info, nil
}

func parseSpliceInsert(data []byte, ptsAdjustment uint64, out *SCTE35) error {
	r := bitReader{data: data}
	eventID, err := r.read(32)
	if err != nil {
		return fmt.Errorf("SCTE-35 splice_insert event id: %w", err)
	}
	out.EventID = uint32(eventID)
	cancel, err := r.read(1)
	if err != nil {
		return fmt.Errorf("SCTE-35 splice_insert cancel flag: %w", err)
	}
	if _, err = r.read(7); err != nil {
		return err
	}
	out.Cancelled = cancel != 0
	if out.Cancelled {
		return requireConsumed(r, "splice_insert")
	}

	outOfNetwork, err := r.read(1)
	if err != nil {
		return err
	}
	programSplice, err := r.read(1)
	if err != nil {
		return err
	}
	durationFlag, err := r.read(1)
	if err != nil {
		return err
	}
	immediate, err := r.read(1)
	if err != nil {
		return err
	}
	if _, err = r.read(4); err != nil {
		return err
	}
	if outOfNetwork != 0 {
		out.Direction = DirectionOut
	} else {
		out.Direction = DirectionIn
	}

	if programSplice != 0 {
		if immediate == 0 {
			out.SplicePTS, err = readSpliceTime(&r, ptsAdjustment)
			if err != nil {
				return err
			}
		}
	} else {
		componentCount, componentErr := r.read(8)
		if componentErr != nil {
			return componentErr
		}
		for range componentCount {
			if _, err = r.read(8); err != nil { // component_tag
				return err
			}
			if immediate == 0 {
				pts, timeErr := readSpliceTime(&r, ptsAdjustment)
				if timeErr != nil {
					return timeErr
				}
				if out.SplicePTS == nil {
					out.SplicePTS = pts
				}
			}
		}
	}

	if durationFlag != 0 {
		autoReturn, readErr := r.read(1)
		if readErr != nil {
			return readErr
		}
		if _, readErr = r.read(6); readErr != nil {
			return readErr
		}
		duration, readErr := r.read(33)
		if readErr != nil {
			return readErr
		}
		out.AutoReturn = autoReturn != 0
		out.BreakDuration90k = uint64Ptr(duration)
	}
	if _, err = r.read(16); err != nil { // unique_program_id
		return err
	}
	if _, err = r.read(8); err != nil { // avail_num
		return err
	}
	if _, err = r.read(8); err != nil { // avails_expected
		return err
	}
	return requireConsumed(r, "splice_insert")
}

func parseTimeSignal(data []byte, ptsAdjustment uint64, out *SCTE35) error {
	r := bitReader{data: data}
	pts, err := readSpliceTime(&r, ptsAdjustment)
	if err != nil {
		return fmt.Errorf("SCTE-35 time_signal: %w", err)
	}
	out.SplicePTS = pts
	return requireConsumed(r, "time_signal")
}

func readSpliceTime(r *bitReader, adjustment uint64) (*uint64, error) {
	specified, err := r.read(1)
	if err != nil {
		return nil, err
	}
	if specified == 0 {
		if _, err := r.read(7); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if _, err := r.read(6); err != nil {
		return nil, err
	}
	pts, err := r.read(33)
	if err != nil {
		return nil, err
	}
	adjusted := (pts + adjustment) & ptsMask
	return &adjusted, nil
}

func parseDescriptors(data []byte, out *SCTE35) error {
	for len(data) > 0 {
		if len(data) < 2 {
			return errors.New("SCTE-35 descriptor header is truncated")
		}
		tag, length := data[0], int(data[1])
		data = data[2:]
		if length > len(data) {
			return errors.New("SCTE-35 descriptor body is truncated")
		}
		body := data[:length]
		data = data[length:]
		if tag != 0x02 {
			continue
		}
		descriptor, err := parseSegmentationDescriptor(body)
		if err != nil {
			return err
		}
		if descriptor.cancelled {
			continue
		}
		if out.Command == CommandTimeSignal {
			out.EventID = descriptor.eventID
			out.Direction = descriptor.direction
		}
		out.SegmentationTypeID = &descriptor.segmentationType
		if out.BreakDuration90k == nil && descriptor.duration != nil {
			out.BreakDuration90k = descriptor.duration
		}
	}
	return nil
}

type segmentationDescriptorInfo struct {
	eventID          uint32
	cancelled        bool
	direction        Direction
	duration         *uint64
	segmentationType uint8
}

func parseSegmentationDescriptor(data []byte) (segmentationDescriptorInfo, error) {
	r := bitReader{data: data}
	identifier, err := r.read(32)
	if err != nil || identifier != 0x43554549 { // CUEI
		return segmentationDescriptorInfo{}, errors.New("SCTE-35 segmentation descriptor has invalid identifier")
	}
	eventID, err := r.read(32)
	if err != nil {
		return segmentationDescriptorInfo{}, err
	}
	cancel, err := r.read(1)
	if err != nil {
		return segmentationDescriptorInfo{}, err
	}
	if _, err = r.read(7); err != nil {
		return segmentationDescriptorInfo{}, err
	}
	info := segmentationDescriptorInfo{eventID: uint32(eventID), cancelled: cancel != 0, direction: DirectionUnknown}
	if info.cancelled {
		return info, nil
	}
	program, err := r.read(1)
	if err != nil {
		return segmentationDescriptorInfo{}, err
	}
	durationFlag, err := r.read(1)
	if err != nil {
		return segmentationDescriptorInfo{}, err
	}
	if _, err = r.read(1); err != nil { // delivery_not_restricted_flag
		return segmentationDescriptorInfo{}, err
	}
	if _, err = r.read(5); err != nil { // delivery restrictions or reserved
		return segmentationDescriptorInfo{}, err
	}
	if program == 0 {
		count, countErr := r.read(8)
		if countErr != nil {
			return segmentationDescriptorInfo{}, countErr
		}
		for range count {
			if _, err = r.read(8); err != nil {
				return segmentationDescriptorInfo{}, err
			}
			if _, err = r.read(7); err != nil {
				return segmentationDescriptorInfo{}, err
			}
			if _, err = r.read(33); err != nil {
				return segmentationDescriptorInfo{}, err
			}
		}
	}
	if durationFlag != 0 {
		duration, durationErr := r.read(40)
		if durationErr != nil {
			return segmentationDescriptorInfo{}, durationErr
		}
		info.duration = uint64Ptr(duration)
	}
	if _, err = r.read(8); err != nil { // segmentation_upid_type
		return segmentationDescriptorInfo{}, err
	}
	upidLength, err := r.read(8)
	if err != nil {
		return segmentationDescriptorInfo{}, err
	}
	if _, err = r.readBytes(int(upidLength)); err != nil {
		return segmentationDescriptorInfo{}, err
	}
	segmentationType, err := r.read(8)
	if err != nil {
		return segmentationDescriptorInfo{}, err
	}
	info.segmentationType = uint8(segmentationType)
	info.direction = segmentationDirection(info.segmentationType)
	if _, err = r.read(8); err != nil { // segment_num
		return segmentationDescriptorInfo{}, err
	}
	if _, err = r.read(8); err != nil { // segments_expected
		return segmentationDescriptorInfo{}, err
	}
	return info, nil
}

func segmentationDirection(t uint8) Direction {
	switch t {
	case 0x22, 0x30, 0x32, 0x34, 0x36, 0x38, 0x3a, 0x40, 0x42, 0x44, 0x46, 0x50:
		return DirectionOut
	case 0x23, 0x31, 0x33, 0x35, 0x37, 0x39, 0x3b, 0x41, 0x43, 0x45, 0x47, 0x51:
		return DirectionIn
	default:
		return DirectionUnknown
	}
}

type bitReader struct {
	data []byte
	bit  int
}

func (r *bitReader) read(count int) (uint64, error) {
	if count < 0 || count > 64 || count > r.remainingBits() {
		return 0, errors.New("SCTE-35 bit field is truncated")
	}
	var value uint64
	for range count {
		value = value<<1 | uint64(r.data[r.bit/8]>>(7-r.bit%8)&1)
		r.bit++
	}
	return value, nil
}

func (r *bitReader) readBytes(count int) ([]byte, error) {
	if r.bit%8 != 0 || count < 0 || count > r.remainingBits()/8 {
		return nil, errors.New("SCTE-35 byte field is truncated or unaligned")
	}
	start := r.bit / 8
	r.bit += count * 8
	return r.data[start : start+count], nil
}

func (r *bitReader) remainingBits() int { return len(r.data)*8 - r.bit }

func requireConsumed(r bitReader, name string) error {
	if r.remainingBits() != 0 {
		return fmt.Errorf("SCTE-35 %s has %d trailing bits", name, r.remainingBits())
	}
	return nil
}

func uint64Ptr(value uint64) *uint64 { return &value }

func mpegCRC32(data []byte) uint32 {
	crc := uint32(0xffffffff)
	for _, value := range data {
		crc ^= uint32(value) << 24
		for range 8 {
			if crc&0x80000000 != 0 {
				crc = crc<<1 ^ 0x04c11db7
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}
