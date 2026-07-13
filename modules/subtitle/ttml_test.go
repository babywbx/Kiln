package subtitle

import (
	"reflect"
	"testing"
	"time"
)

func TestParseTTMLAlignsCueToSampleTimelineAndExtractsText(t *testing.T) {
	t.Parallel()

	document := []byte(`<tt xmlns="http://www.w3.org/ns/ttml"><body begin="1s"><div><p xml:id="greeting" begin="500ms" dur="2s">Tom &amp; <span>Jerry</span><br/>你好 &lt;世界&gt;</p></div></body></tt>`)
	cues, err := ParseTTML(document, TTMLParseOptions{
		BaseTime:        10 * time.Second,
		DefaultDuration: 6 * time.Second,
	})
	if err != nil {
		t.Fatalf("ParseTTML: %v", err)
	}
	want := []Cue{{
		ID:    "greeting",
		Start: 11500 * time.Millisecond,
		End:   13500 * time.Millisecond,
		Text:  "Tom & Jerry\n你好 <世界>",
	}}
	if !reflect.DeepEqual(cues, want) {
		t.Fatalf("ParseTTML cues = %#v, want %#v", cues, want)
	}
}

func TestParseTTMLUsesDocumentTimingParametersAndParentWindow(t *testing.T) {
	t.Parallel()

	document := []byte(`<tt xmlns="http://www.w3.org/ns/ttml" xmlns:ttp="http://www.w3.org/ns/ttml#parameter" ttp:frameRate="30" ttp:frameRateMultiplier="1000 1001"><body end="5s"><div><p begin="30f">first</p><p begin="4s" end="7s">second</p></div></body></tt>`)
	cues, err := ParseTTML(document, TTMLParseOptions{BaseTime: 20 * time.Second})
	if err != nil {
		t.Fatalf("ParseTTML: %v", err)
	}
	want := []Cue{
		{Start: 21001 * time.Millisecond, End: 25 * time.Second, Text: "first"},
		{Start: 24 * time.Second, End: 25 * time.Second, Text: "second"},
	}
	if !reflect.DeepEqual(cues, want) {
		t.Fatalf("ParseTTML cues = %#v, want %#v", cues, want)
	}
}

func TestParseTTMLDerivesTickRateFromExplicitFrameAndSubframeRates(t *testing.T) {
	t.Parallel()

	document := []byte(`<tt xmlns="http://www.w3.org/ns/ttml" xmlns:ttp="http://www.w3.org/ns/ttml#parameter" ttp:frameRate="25" ttp:subFrameRate="2"><body><p begin="50t" dur="50t">tick</p></body></tt>`)
	cues, err := ParseTTML(document, TTMLParseOptions{})
	if err != nil {
		t.Fatalf("ParseTTML: %v", err)
	}
	want := []Cue{{Start: time.Second, End: 2 * time.Second, Text: "tick"}}
	if !reflect.DeepEqual(cues, want) {
		t.Fatalf("ParseTTML cues = %#v, want %#v", cues, want)
	}
}

func TestParseTTMLRejectsMalformedDocumentsAndCueTimes(t *testing.T) {
	t.Parallel()

	for _, document := range [][]byte{
		[]byte(`<tt><body><p begin="1s">broken</body></tt>`),
		[]byte(`<tt><body><p begin="later" end="2s">broken</p></body></tt>`),
	} {
		if _, err := ParseTTML(document, TTMLParseOptions{}); err == nil {
			t.Errorf("ParseTTML(%q) unexpectedly succeeded", document)
		}
	}
}
