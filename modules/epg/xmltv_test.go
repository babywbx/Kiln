package epg_test

import (
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

func parseSingleProgramme(t *testing.T, start, timezone string) epg.Programme {
	t.Helper()
	raw := `<tv><programme channel="one" start="` + start + `"><title>test</title></programme></tv>`
	doc, err := epg.Parse(strings.NewReader(raw), timezone)
	if err != nil {
		t.Fatal(err)
	}
	return doc.Programmes[0]
}
