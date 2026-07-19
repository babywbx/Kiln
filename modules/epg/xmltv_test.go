package epg_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/babywbx/kiln/modules/epg"
)

func TestParseTreatsUTCAndPlusEightAsTheSameInstant(t *testing.T) {
	t.Parallel()

	utc := parseSingleProgramme(t, `20260713000000 +0000`, "Asia/Hong_Kong")
	plusEight := parseSingleProgramme(t, `20260713080000 +0800`, "Asia/Hong_Kong")
	if !utc.Start.Time.Equal(plusEight.Start.Time) {
		t.Fatalf("timestamps differ: %s != %s", utc.Start.Time, plusEight.Start.Time)
	}

	got, err := epg.Marshal(&epg.Document{Programmes: []epg.Programme{utc, plusEight}})
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	if !strings.Contains(text, `start="20260713000000 +0000"`) {
		t.Errorf("UTC offset was not preserved:\n%s", text)
	}
	if !strings.Contains(text, `start="20260713080000 +0800"`) {
		t.Errorf("+0800 offset was not preserved:\n%s", text)
	}
}

func TestParseUsesSourceTimezoneWhenOffsetIsMissing(t *testing.T) {
	t.Parallel()

	programme := parseSingleProgramme(t, `20260713080000`, "Asia/Hong_Kong")
	want := time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC)
	if !programme.Start.Time.Equal(want) {
		t.Fatalf("start = %s, want %s", programme.Start.Time, want)
	}
	if programme.Start.Offset != "+0800" {
		t.Fatalf("fallback offset = %q, want +0800", programme.Start.Offset)
	}
}

func TestParseAcceptsEveryXMLTVTimestampPrecision(t *testing.T) {
	t.Parallel()

	tests := []struct {
		value string
		want  time.Time
	}{
		{value: "20260713", want: time.Date(2026, 7, 12, 16, 0, 0, 0, time.UTC)},
		{value: "2026071308", want: time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC)},
		{value: "202607130830", want: time.Date(2026, 7, 13, 0, 30, 0, 0, time.UTC)},
		{value: "20260713083045", want: time.Date(2026, 7, 13, 0, 30, 45, 0, time.UTC)},
		{value: " 20260713083045\t+0800 ", want: time.Date(2026, 7, 13, 0, 30, 45, 0, time.UTC)},
	}
	for _, test := range tests {
		programme := parseSingleProgramme(t, test.value, "Asia/Hong_Kong")
		if !programme.Start.Time.Equal(test.want) {
			t.Errorf("start %q = %s, want %s", test.value, programme.Start.Time, test.want)
		}
	}
}

func TestParseRejectsMalformedXMLTVTimestamps(t *testing.T) {
	t.Parallel()

	for _, value := range []string{
		"2026071",
		"202607130",
		"20260713080",
		"2026071308300",
		"202607130830450",
		"2026071a",
		"20260713083045 UTC",
		"20260713083045 +080",
		"20260713083045 +08x0",
	} {
		raw := `<tv><programme channel="one" start="` + value + `"/></tv>`
		if _, err := epg.Parse(strings.NewReader(raw), "Asia/Hong_Kong"); err == nil {
			t.Errorf("Parse accepted malformed timestamp %q", value)
		}
	}
}

func TestMarshalProducesLegalEmptyTV(t *testing.T) {
	t.Parallel()

	raw, err := epg.Marshal(&epg.Document{GeneratorInfoName: "Kiln"})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := epg.Parse(strings.NewReader(string(raw)), "Asia/Hong_Kong")
	if err != nil {
		t.Fatalf("empty document did not round trip: %v\n%s", err, raw)
	}
	if len(parsed.Channels) != 0 || len(parsed.Programmes) != 0 {
		t.Fatalf("empty document gained content: %+v", parsed)
	}
}

func TestParsePreservesProgrammeBody(t *testing.T) {
	t.Parallel()

	raw := `<tv><channel id="one"><display-name lang="zh">翡翠台</display-name></channel>` +
		`<programme channel="one" start="20260713080000 +0800" pdc-start="20260713080000 +0800" showview="123"><title lang="zh">早晨</title>` +
		`<category>News</category><episode-num system="onscreen">1</episode-num></programme></tv>`
	doc, err := epg.Parse(strings.NewReader(raw), "Asia/Hong_Kong")
	if err != nil {
		t.Fatal(err)
	}
	if got := doc.Channels[0].DisplayNames[0].Value; got != "翡翠台" {
		t.Fatalf("display name = %q", got)
	}
	if got := doc.Programmes[0].Titles[0].Value; got != "早晨" {
		t.Fatalf("title = %q", got)
	}
	if doc.Programmes[0].PDCStart == nil || doc.Programmes[0].PDCStart.Offset != "+0800" || doc.Programmes[0].ShowView != "123" {
		t.Fatalf("programme attributes = %+v", doc.Programmes[0])
	}
	roundTrip, err := epg.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		"<category>News</category>", `<episode-num system="onscreen">1</episode-num>`,
		`pdc-start="20260713080000 +0800"`, `showview="123"`,
	} {
		if !strings.Contains(string(roundTrip), fragment) {
			t.Errorf("round trip lost %s:\n%s", fragment, roundTrip)
		}
	}
}

func TestParseBytesPreservesStructuredFieldsAndExtensions(t *testing.T) {
	t.Parallel()

	raw := []byte(`<tv source-info-name="Example" generator-info-name="Generator">` +
		`<channel id="one"><display-name lang="zh">翡翠台</display-name><icon src="logo.png" width="64"/>` +
		`<extension xmlns="urn:example">channel data</extension></channel>` +
		`<programme channel="one" start="20260713080000 +0800" stop="20260713083000 +0800">` +
		`<title lang="zh">早晨</title><desc>新聞</desc><episode-num system="onscreen">1</episode-num></programme></tv>`)
	doc, err := epg.ParseBytes(raw, "Asia/Hong_Kong")
	if err != nil {
		t.Fatal(err)
	}
	if doc.SourceInfoName != "Example" || doc.GeneratorInfoName != "Generator" {
		t.Fatalf("document metadata = %+v", doc)
	}
	if got := doc.Channels[0].DisplayNames[0]; got.Lang != "zh" || got.Value != "翡翠台" {
		t.Fatalf("display name = %+v", got)
	}
	if got := doc.Channels[0].Icons[0]; got.Src != "logo.png" || got.Width != "64" {
		t.Fatalf("icon = %+v", got)
	}
	if got := doc.Programmes[0].Descriptions[0].Value; got != "新聞" {
		t.Fatalf("description = %q", got)
	}

	roundTrip, err := epg.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		`<extension xmlns="urn:example">channel data</extension>`,
		`<episode-num system="onscreen">1</episode-num>`,
		`stop="20260713083000 +0800"`,
	} {
		if !strings.Contains(string(roundTrip), fragment) {
			t.Errorf("ParseBytes round trip lost %s:\n%s", fragment, roundTrip)
		}
	}
}

func TestParseBytesMatchesReaderParse(t *testing.T) {
	t.Parallel()

	fixtures := []string{
		`<?xml version="1.0"?><tv date="20260713"><channel id="empty"/></tv>`,
		`<tv><channel id="one"><display-name><![CDATA[One & Only]]></display-name><!-- note --><extension key="value"/></channel>` +
			`<programme channel="one" start="2026071308"><title>Morning</title><credits><director>Example</director></credits></programme></tv>`,
		`<tv xmlns="urn:xmltv"><metadata><channel id="nested"><display-name>Nested</display-name></channel></metadata>` +
			`<channel id="top"><display-name lang="en">Top</display-name><url>https://example.test/?a=1&amp;b=2</url></channel></tv>`,
	}
	for _, raw := range fixtures {
		fromReader, err := epg.Parse(strings.NewReader(raw), "Asia/Hong_Kong")
		if err != nil {
			t.Fatalf("Parse failed: %v", err)
		}
		fromBytes, err := epg.ParseBytes([]byte(raw), "Asia/Hong_Kong")
		if err != nil {
			t.Fatalf("ParseBytes failed: %v", err)
		}
		if !reflect.DeepEqual(fromBytes, fromReader) {
			t.Fatalf("ParseBytes differs from Parse:\nbytes:  %+v\nreader: %+v", fromBytes, fromReader)
		}
	}
}

func parseSingleProgramme(t *testing.T, start, timezone string) epg.Programme {
	t.Helper()
	raw := `<tv><programme channel="one" start="` + start + `"><title>test</title></programme></tv>`
	doc, err := epg.Parse(strings.NewReader(raw), timezone)
	if err != nil {
		t.Fatal(err)
	}
	return doc.Programmes[0]
}
