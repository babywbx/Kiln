package timedmeta_test

import (
	"encoding/binary"
	"strings"
	"testing"
	"time"

	"github.com/babywbx/kiln/modules/timedmeta"
)

func TestFromEmsgMapsVersionZeroAgainstSegmentAnchor(t *testing.T) {
	payload := []byte{1, 2, 3}
	event, err := timedmeta.FromEmsg(timedmeta.Emsg{
		Version:                 0,
		TimeScale:               1000,
		PresentationTimeDelta:   250,
		SegmentPresentationTime: 9000,
		EventDuration:           500,
		ID:                      7,
		SchemeIDURI:             "urn:example:event",
		Value:                   "example",
		MessageData:             payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	if event.PresentationTime != 9250 || event.TimeScale != 1000 || event.Duration != 500 {
		t.Fatalf("unexpected timing: %+v", event)
	}
	if event.Kind != timedmeta.KindEmsg || event.ID != 7 || event.Value != "example" {
		t.Fatalf("unexpected mapping: %+v", event)
	}
	payload[0] = 9
	if event.Payload[0] != 1 {
		t.Fatal("event retained the caller's mutable payload")
	}
}

func TestFromEmsgMapsVersionOneAndRecognizesAOMID3(t *testing.T) {
	payload := []byte("ID3\x04\x00")
	event, err := timedmeta.FromEmsg(timedmeta.Emsg{
		Version:          1,
		TimeScale:        90000,
		PresentationTime: 180000,
		EventDuration:    9000,
		ID:               11,
		SchemeIDURI:      "https://aomedia.org/emsg/ID3",
		MessageData:      payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	if event.Kind != timedmeta.KindID3 || event.PresentationTime != 180000 {
		t.Fatalf("unexpected ID3 event: %+v", event)
	}
	if string(event.Payload) != string(payload) {
		t.Fatalf("payload = %q", event.Payload)
	}
}

func TestFromEmsgRejectsInvalidTiming(t *testing.T) {
	tests := []timedmeta.Emsg{
		{Version: 2, TimeScale: 1},
		{Version: 1, TimeScale: 0},
		{Version: 0, TimeScale: 1, SegmentPresentationTime: ^uint64(0), PresentationTimeDelta: 1},
	}
	for _, input := range tests {
		if _, err := timedmeta.FromEmsg(input); err == nil {
			t.Fatalf("FromEmsg(%+v) succeeded", input)
		}
	}
}

func TestParseSCTE35SpliceInsertOutWithDuration(t *testing.T) {
	payload := spliceInsertSection(0x10203040, true, true, 180000, 900000)
	info, err := timedmeta.ParseSCTE35(payload)
	if err != nil {
		t.Fatal(err)
	}
	if info.Command != timedmeta.CommandSpliceInsert || info.EventID != 0x10203040 {
		t.Fatalf("unexpected command: %+v", info)
	}
	if info.Direction != timedmeta.DirectionOut {
		t.Fatalf("direction = %s", info.Direction)
	}
	if info.SplicePTS == nil || *info.SplicePTS != 180000 {
		t.Fatalf("splice pts = %v", info.SplicePTS)
	}
	if info.BreakDuration90k == nil || *info.BreakDuration90k != 900000 {
		t.Fatalf("break duration = %v", info.BreakDuration90k)
	}
}

func TestParseSCTE35TimeSignalUsesSegmentationDescriptorForIn(t *testing.T) {
	payload := timeSignalSection(270000, segmentationDescriptor(0x55667788, 0x23))
	info, err := timedmeta.ParseSCTE35(payload)
	if err != nil {
		t.Fatal(err)
	}
	if info.Command != timedmeta.CommandTimeSignal || info.EventID != 0x55667788 {
		t.Fatalf("unexpected command: %+v", info)
	}
	if info.Direction != timedmeta.DirectionIn {
		t.Fatalf("direction = %s", info.Direction)
	}
	if info.SegmentationTypeID == nil || *info.SegmentationTypeID != 0x23 {
		t.Fatalf("segmentation type = %v", info.SegmentationTypeID)
	}
}

func TestFromEmsgRecognizesSCTE35AndPreservesRawCommand(t *testing.T) {
	payload := spliceInsertSection(99, false, false, 0, 0)
	event, err := timedmeta.FromEmsg(timedmeta.Emsg{
		Version:          1,
		TimeScale:        90000,
		PresentationTime: 450000,
		ID:               3,
		SchemeIDURI:      "urn:scte:scte35:2013:bin",
		MessageData:      payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	if event.Kind != timedmeta.KindSCTE35 || event.SCTE35 == nil {
		t.Fatalf("unexpected event: %+v", event)
	}
	if event.SCTE35.Direction != timedmeta.DirectionIn || string(event.Payload) != string(payload) {
		t.Fatalf("unexpected SCTE-35 mapping: %+v", event)
	}
}

func TestParseSCTE35RejectsMalformedInput(t *testing.T) {
	valid := spliceInsertSection(1, true, false, 0, 0)
	tests := [][]byte{
		nil,
		{0xfc, 0x30, 0x01},
		append([]byte(nil), valid[:len(valid)-1]...),
		append(append([]byte(nil), valid...), 0),
		withSectionCRC(valid, func(section []byte) { section[1] |= 0x80 }),
		withSectionCRC(valid, func(section []byte) { section[3] = 1 }),
		func() []byte {
			bad := append([]byte(nil), valid...)
			bad[5] ^= 0x80
			return bad
		}(),
	}
	for _, payload := range tests {
		if _, err := timedmeta.ParseSCTE35(payload); err == nil {
			t.Fatalf("ParseSCTE35(%x) succeeded", payload)
		}
	}
}

func withSectionCRC(section []byte, mutate func([]byte)) []byte {
	out := append([]byte(nil), section...)
	mutate(out)
	crc := mpegCRC32(out[:len(out)-4])
	binary.BigEndian.PutUint32(out[len(out)-4:], crc)
	return out
}

func TestDateRangeUsesExplicitClockAnchor(t *testing.T) {
	duration := uint64(180000)
	payload := spliceInsertSection(42, true, true, 0, duration)
	event, err := timedmeta.FromEmsg(timedmeta.Emsg{
		Version:          1,
		TimeScale:        1000,
		PresentationTime: 11000,
		SchemeIDURI:      "urn:scte:scte35:2013:bin",
		MessageData:      payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	anchorAt := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	dr, ok, err := event.DateRange(timedmeta.ClockAnchor{
		WallClock:        anchorAt,
		PresentationTime: 90000,
		TimeScale:        90000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("SCTE-35 event did not produce a date range")
	}
	if want := anchorAt.Add(10 * time.Second); !dr.StartDate.Equal(want) {
		t.Fatalf("start = %s, want %s", dr.StartDate, want)
	}
	if dr.PlannedDuration == nil || *dr.PlannedDuration != 2*time.Second {
		t.Fatalf("planned duration = %v", dr.PlannedDuration)
	}
	if dr.SCTE35Out == "" || dr.SCTE35In != "" || dr.SCTE35Cmd != "" {
		t.Fatalf("unexpected SCTE attributes: %+v", dr)
	}
	tag := dr.MarshalTag()
	for _, want := range []string{
		`#EXT-X-DATERANGE:ID="scte35-42"`,
		`CLASS="com.apple.hls.scte35"`,
		`START-DATE="2026-07-13T12:00:10.000Z"`,
		`PLANNED-DURATION=2.000000`,
		`SCTE35-OUT="0xFC`,
	} {
		if !strings.Contains(tag, want) {
			t.Fatalf("tag missing %q:\n%s", want, tag)
		}
	}
}

func TestDateRangeRejectsMissingAnchorTimescale(t *testing.T) {
	event := timedmeta.Event{TimeScale: 1000}
	if _, _, err := event.DateRange(timedmeta.ClockAnchor{}); err == nil {
		t.Fatal("DateRange accepted a zero anchor timescale")
	}
}

func TestMergeDateRangeClosesOutObservationWithoutChangingItsStart(t *testing.T) {
	start := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	end := start.Add(30 * time.Second)
	planned := 45 * time.Second
	out := timedmeta.DateRange{
		ID: "scte35-42", Class: "com.apple.hls.scte35", StartDate: start,
		PlannedDuration: &planned, SCTE35Out: "0xFC01",
	}
	in := timedmeta.DateRange{
		ID: "scte35-42", Class: "com.apple.hls.scte35", StartDate: end,
		SCTE35In: "0xFC02",
	}
	merged := timedmeta.MergeDateRange(out, in)
	if !merged.StartDate.Equal(start) || merged.EndDate == nil || !merged.EndDate.Equal(end) {
		t.Fatalf("merged range times = %s..%v", merged.StartDate, merged.EndDate)
	}
	if merged.Duration == nil || *merged.Duration != 30*time.Second {
		t.Fatalf("merged duration = %v", merged.Duration)
	}
	if merged.SCTE35Out != "0xFC01" || merged.SCTE35In != "0xFC02" {
		t.Fatalf("merged commands = %+v", merged)
	}
}

func FuzzParseSCTE35(f *testing.F) {
	f.Add(spliceInsertSection(1, true, true, 90000, 180000))
	f.Add(timeSignalSection(270000, segmentationDescriptor(2, 0x23)))
	f.Add([]byte{0xfc})
	f.Fuzz(func(_ *testing.T, payload []byte) {
		_, _ = timedmeta.ParseSCTE35(payload)
	})
}

type bitWriter struct {
	data []byte
	bits int
}

func (w *bitWriter) write(value uint64, count int) {
	for i := count - 1; i >= 0; i-- {
		if w.bits%8 == 0 {
			w.data = append(w.data, 0)
		}
		if value&(uint64(1)<<i) != 0 {
			w.data[len(w.data)-1] |= 1 << (7 - w.bits%8)
		}
		w.bits++
	}
}

func (w *bitWriter) bytes(raw []byte) {
	if w.bits%8 != 0 {
		panic("unaligned test bit writer")
	}
	for _, b := range raw {
		w.write(uint64(b), 8)
	}
}

func spliceTime(w *bitWriter, pts uint64) {
	w.write(1, 1)
	w.write(0x3f, 6)
	w.write(pts, 33)
}

func spliceInsertSection(eventID uint32, out, withDuration bool, pts, duration uint64) []byte {
	var command bitWriter
	command.write(uint64(eventID), 32)
	command.write(0, 1)
	command.write(0x7f, 7)
	command.write(boolBit(out), 1)
	command.write(1, 1)
	command.write(boolBit(withDuration), 1)
	immediate := pts == 0
	command.write(boolBit(immediate), 1)
	command.write(0xf, 4)
	if !immediate {
		spliceTime(&command, pts)
	}
	if withDuration {
		command.write(1, 1)
		command.write(0x3f, 6)
		command.write(duration, 33)
	}
	command.write(1, 16)
	command.write(1, 8)
	command.write(1, 8)
	return spliceSection(byte(timedmeta.CommandSpliceInsert), command.data, nil)
}

func timeSignalSection(pts uint64, descriptors []byte) []byte {
	var command bitWriter
	spliceTime(&command, pts)
	return spliceSection(byte(timedmeta.CommandTimeSignal), command.data, descriptors)
}

func segmentationDescriptor(eventID uint32, segmentationType byte) []byte {
	var body bitWriter
	body.bytes([]byte("CUEI"))
	body.write(uint64(eventID), 32)
	body.write(0, 1)
	body.write(0x7f, 7)
	body.write(1, 1)
	body.write(0, 1)
	body.write(1, 1)
	body.write(0x1f, 5)
	body.write(0, 8)
	body.write(0, 8)
	body.write(uint64(segmentationType), 8)
	body.write(1, 8)
	body.write(1, 8)
	return append([]byte{0x02, byte(len(body.data))}, body.data...)
}

func spliceSection(commandType byte, command, descriptors []byte) []byte {
	var section bitWriter
	section.write(0xfc, 8)
	section.write(0, 1)
	section.write(0, 1)
	section.write(3, 2)
	section.write(0, 12)
	section.write(0, 8)
	section.write(0, 1)
	section.write(0, 6)
	section.write(0, 33)
	section.write(0, 8)
	section.write(0xfff, 12)
	section.write(uint64(len(command)), 12)
	section.write(uint64(commandType), 8)
	section.bytes(command)
	section.write(uint64(len(descriptors)), 16)
	section.bytes(descriptors)

	raw := section.data
	sectionLength := len(raw) - 3 + 4
	raw[1] = raw[1]&0xf0 | byte(sectionLength>>8)&0x0f
	raw[2] = byte(sectionLength)
	crc := mpegCRC32(raw)
	var trailer [4]byte
	binary.BigEndian.PutUint32(trailer[:], crc)
	return append(raw, trailer[:]...)
}

func boolBit(v bool) uint64 {
	if v {
		return 1
	}
	return 0
}

func mpegCRC32(data []byte) uint32 {
	crc := uint32(0xffffffff)
	for _, b := range data {
		crc ^= uint32(b) << 24
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
