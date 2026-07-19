package epg_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/babywbx/kiln/modules/epg"
)

var benchmarkXMLTVDocument *epg.Document

func BenchmarkParseXMLTV(b *testing.B) {
	fixtures := []struct {
		name                 string
		channels             int
		programmesPerChannel int
	}{
		{name: "Small", channels: 8, programmesPerChannel: 12},
		{name: "Large", channels: 64, programmesPerChannel: 96},
	}
	for _, fixture := range fixtures {
		data := makeXMLTVFixture(fixture.channels, fixture.programmesPerChannel)
		b.Run(fixture.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				document, err := epg.ParseBytes(data, "Asia/Hong_Kong")
				if err != nil {
					b.Fatal(err)
				}
				benchmarkXMLTVDocument = document
			}
		})
	}
}

func makeXMLTVFixture(channelCount, programmesPerChannel int) []byte {
	var xml strings.Builder
	xml.Grow(channelCount * programmesPerChannel * 256)
	xml.WriteString(`<tv source-info-name="Kiln benchmark">`)
	for channelIndex := range channelCount {
		id := "channel-" + strconv.Itoa(channelIndex)
		xml.WriteString(`<channel id="`)
		xml.WriteString(id)
		xml.WriteString(`"><display-name lang="en">Channel `)
		xml.WriteString(strconv.Itoa(channelIndex))
		xml.WriteString(`</display-name><icon src="https://example.test/logo.png"/><url>https://example.test/</url></channel>`)
		for programmeIndex := range programmesPerChannel {
			xml.WriteString(`<programme channel="`)
			xml.WriteString(id)
			xml.WriteString(`" start="20260713080000 +0800" stop="20260713083000 +0800"><title lang="en">Programme `)
			xml.WriteString(strconv.Itoa(programmeIndex))
			xml.WriteString(`</title><sub-title>Episode</sub-title><desc>A representative programme description for allocation measurements.</desc><category>News</category><episode-num system="onscreen">1</episode-num></programme>`)
		}
	}
	xml.WriteString(`</tv>`)
	return []byte(xml.String())
}
