package mediaurl

import "testing"

func TestResolveRefPreservesRelativeMediaQueryAndFragment(t *testing.T) {
	tests := []struct {
		name string
		ref  string
		want string
	}{
		{name: "segment", ref: "seg.ts?token=segment", want: "https://origin.example/live/seg.ts?token=segment"},
		{name: "key", ref: "keys/key.bin?token=key#rotation", want: "https://origin.example/live/keys/key.bin?token=key#rotation"},
		{name: "map", ref: "/init.mp4?token=map", want: "https://origin.example/init.mp4?token=map"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ResolveRef("https://origin.example/live/index.m3u8?parent=ignored", test.ref)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("ResolveRef() = %q, want %q", got, test.want)
			}
		})
	}
}
