package media

import (
	"errors"
	"reflect"
	"testing"
)

func TestSelectVariantAppliesDownloadConstraintsAndBandwidthOrder(t *testing.T) {
	variants := []variant{
		{Audio: "audio-alac-stereo-96000-16", Bandwidth: 4_000_000, SampleRate: 96_000, BitDepth: 16},
		{Audio: "audio-alac-stereo-48000-24", Bandwidth: 5_000_000, SampleRate: 48_000, BitDepth: 24},
		{Audio: "audio-alac-stereo-192000-24", Bandwidth: 9_000_000, SampleRate: 192_000, BitDepth: 24},
	}
	original := append([]variant(nil), variants...)

	tests := []struct {
		name          string
		maxSampleRate int
		maxBitDepth   int
		wantAudio     string
		wantNotFound  bool
	}{
		{name: "unlimited chooses highest bandwidth", wantAudio: "audio-alac-stereo-192000-24"},
		{name: "sample rate limit then bandwidth", maxSampleRate: 96_000, maxBitDepth: 24, wantAudio: "audio-alac-stereo-48000-24"},
		{name: "bit depth limit", maxBitDepth: 16, wantAudio: "audio-alac-stereo-96000-16"},
		{name: "all filtered", maxSampleRate: 44_100, maxBitDepth: 16, wantNotFound: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selected, err := selectVariant(variants, "alac", tt.maxSampleRate, tt.maxBitDepth)
			var notFound codecNotFoundError
			if tt.wantNotFound {
				if !errors.As(err, &notFound) {
					t.Fatalf("error = %v, want codecNotFoundError", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if selected.Audio != tt.wantAudio {
				t.Fatalf("selected = %q, want %q", selected.Audio, tt.wantAudio)
			}
		})
	}
	if !reflect.DeepEqual(variants, original) {
		t.Fatalf("selectVariant mutated shared variants: got %#v want %#v", variants, original)
	}
}
