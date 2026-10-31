package format

import (
	"slices"
	"testing"
)

func TestADownloadAsksBetweenLosslessAndDolbyOnlyWhenBothApply(t *testing.T) {
	both := []string{"atmos", "lossless", "lossy-stereo"}
	for _, tc := range []struct {
		traits, priority, want []string
	}{
		{both, []string{"alac", "ec3"}, []string{"alac", "ec3"}},
		{both, []string{"ec3", "alac", "aac"}, []string{"ec3", "alac"}},
		{both, []string{"alac"}, nil},
		{both, []string{"alac", "aac"}, nil},
		{[]string{"lossless", "lossy-stereo"}, []string{"alac", "ec3"}, nil},
		{[]string{"atmos", "lossy-stereo"}, []string{"alac", "ec3"}, nil},
	} {
		if got := QualityChoices(tc.traits, tc.priority); !slices.Equal(got, tc.want) {
			t.Errorf("traits %v, priority %v: asks %v, want %v", tc.traits, tc.priority, got, tc.want)
		}
	}
}
