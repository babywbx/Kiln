package cmaf

import (
	"bytes"
	"os"
	"testing"

	"github.com/Eyevinn/mp4ff/mp4"
)

func decodeFixtureInit(t *testing.T, dir string) *mp4.File {
	t.Helper()
	raw, err := os.ReadFile(fixturePath(dir, "init-stream0.m4s"))
	if err != nil {
		t.Fatal(err)
	}
	f, err := mp4.DecodeFile(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func encodeInit(t *testing.T, f *mp4.File) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := f.Init.Encode(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func dropChild(children []mp4.Box, typ string) []mp4.Box {
	kept := make([]mp4.Box, 0, len(children))
	for _, c := range children {
		if c.Type() != typ {
			kept = append(kept, c)
		}
	}
	return kept
}

func TestInitWithoutSampleDescriptionIsRejected(t *testing.T) {
	f := decodeFixtureInit(t, "hevc")
	stbl := f.Init.Moov.Traks[0].Mdia.Minf.Stbl
	stbl.Children = dropChild(stbl.Children, "stsd")
	stbl.Stsd = nil

	_, err := ParseInit(encodeInit(t, f))
	if err == nil {
		t.Fatal("an init segment whose stbl carries no stsd was accepted")
	}
	u, ok := Unsupported(err)
	if !ok || u.Reason != ReasonNotFragmented {
		t.Fatalf("err = %v, want unsupported %s", err, ReasonNotFragmented)
	}
}

func TestInitWhoseProtectionSchemeIsMissingIsRejected(t *testing.T) {
	f := decodeFixtureInit(t, "hevc")
	stsd := f.Init.Moov.Traks[0].Mdia.Minf.Stbl.Stsd
	var sinf *mp4.SinfBox
	for _, child := range stsd.Children {
		if v, ok := child.(*mp4.VisualSampleEntryBox); ok {
			sinf = v.Sinf
		}
	}
	if sinf == nil || sinf.Schm == nil {
		t.Fatalf("fixture is not an encrypted visual sample entry with a schm")
	}
	sinf.Children = dropChild(sinf.Children, "schm")
	sinf.Schm = nil

	_, err := ParseInit(encodeInit(t, f))
	if err == nil {
		t.Fatal("an encrypted sample entry with no schm was accepted")
	}
	if _, ok := Unsupported(err); !ok {
		t.Fatalf("err = %v, want an unsupported error rather than a panic", err)
	}
}
