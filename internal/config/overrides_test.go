package config

import (
	"reflect"
	"strings"
	"testing"

	"amdl/internal/domain"
)

func TestApplyDownloadOverridesNilReturnsBase(t *testing.T) {
	base := Default().Download
	if got := ApplyDownloadOverrides(base, nil); got.SongFileFormat != base.SongFileFormat || got.EmbedCover != base.EmbedCover {
		t.Fatalf("got = %+v, want base unchanged", got)
	}
}

func TestApplyDownloadOverridesSetsFieldsAndCopiesSlices(t *testing.T) {
	base := Default().Download
	priority := []string{"aac"}
	embed := false
	retries := 7
	o := &domain.DownloadOverrides{QualityPriority: &priority, EmbedCover: &embed, Retries: &retries}

	got := ApplyDownloadOverrides(base, o)
	if len(got.QualityPriority) != 1 || got.QualityPriority[0] != "aac" {
		t.Fatalf("quality priority = %v", got.QualityPriority)
	}
	if got.EmbedCover || got.Retries != 7 {
		t.Fatalf("embed=%v retries=%d", got.EmbedCover, got.Retries)
	}
	// Untouched fields keep base values.
	if got.SongFileFormat != base.SongFileFormat || got.DownloadsDir != base.DownloadsDir {
		t.Fatalf("untouched fields changed: %+v", got)
	}
	// The applied slice must not alias the overlay's backing array.
	priority[0] = "mutated"
	if got.QualityPriority[0] != "aac" {
		t.Fatal("applied config aliases the override slice")
	}
}

// TestApplyDownloadOverridesCoversEveryField reflects over the overlay so a
// field added to DownloadOverrides but forgotten in ApplyDownloadOverrides
// (or missing a same-named DownloadConfig counterpart) fails the build's
// tests instead of silently becoming a no-op snapshot.
func TestApplyDownloadOverridesCoversEveryField(t *testing.T) {
	base := Default().Download
	baseVal := reflect.ValueOf(base)

	var o domain.DownloadOverrides
	ov := reflect.ValueOf(&o).Elem()
	for i := 0; i < ov.NumField(); i++ {
		field := ov.Type().Field(i)
		target := baseVal.FieldByName(field.Name)
		if !target.IsValid() {
			t.Fatalf("DownloadConfig has no field named %s", field.Name)
		}
		// Sentinel values differ from the base so a missing Apply branch is
		// detectable even for booleans.
		elem := reflect.New(field.Type.Elem())
		switch elem.Elem().Kind() {
		case reflect.String:
			elem.Elem().SetString(target.String() + "-override")
		case reflect.Bool:
			elem.Elem().SetBool(!target.Bool())
		case reflect.Int:
			elem.Elem().SetInt(target.Int() + 1)
		case reflect.Slice:
			slice := reflect.MakeSlice(field.Type.Elem(), 1, 1)
			slice.Index(0).SetString("sentinel")
			elem.Elem().Set(slice)
		default:
			t.Fatalf("field %s has unhandled kind %s; extend this test", field.Name, elem.Elem().Kind())
		}
		ov.Field(i).Set(elem)
	}

	applied := reflect.ValueOf(ApplyDownloadOverrides(base, &o))
	for i := 0; i < ov.NumField(); i++ {
		field := ov.Type().Field(i)
		want := ov.Field(i).Elem().Interface()
		got := applied.FieldByName(field.Name).Interface()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("ApplyDownloadOverrides drops field %s: got %v, want %v", field.Name, got, want)
		}
	}
}

func TestValidateDownloadOverrides(t *testing.T) {
	strPtr := func(v string) *string { return &v }
	intPtr := func(v int) *int { return &v }
	slicePtr := func(v ...string) *[]string { return &v }

	tests := []struct {
		name    string
		o       *domain.DownloadOverrides
		wantErr string
	}{
		{name: "nil is valid"},
		{name: "valid overlay", o: &domain.DownloadOverrides{CoverFormat: strPtr("png"), Retries: intPtr(5)}},
		{name: "bad codec", o: &domain.DownloadOverrides{QualityPriority: slicePtr("flac")}, wantErr: "unsupported codec"},
		{name: "empty quality priority", o: &domain.DownloadOverrides{QualityPriority: slicePtr()}, wantErr: "quality_priority"},
		{name: "bad cover format", o: &domain.DownloadOverrides{CoverFormat: strPtr("webp")}, wantErr: "cover_format"},
		{name: "bad lyrics format", o: &domain.DownloadOverrides{LyricsFormat: strPtr("srt")}, wantErr: "lyrics_format"},
		{name: "bad lyrics extras", o: &domain.DownloadOverrides{LyricsExtras: slicePtr("romaji")}, wantErr: "lyrics_extras"},
		{name: "empty song format", o: &domain.DownloadOverrides{SongFileFormat: strPtr("  ")}, wantErr: "song_file_format"},
		{name: "parallel too low", o: &domain.DownloadOverrides{MaxParallelTracks: intPtr(0)}, wantErr: "max_parallel_tracks"},
		{name: "parallel too high", o: &domain.DownloadOverrides{MaxParallelTracks: intPtr(17)}, wantErr: "max_parallel_tracks"},
		{name: "negative retries", o: &domain.DownloadOverrides{Retries: intPtr(-1)}, wantErr: "retries"},
		{name: "excessive retries", o: &domain.DownloadOverrides{Retries: intPtr(11)}, wantErr: "retries"},
		{name: "bad cover size", o: &domain.DownloadOverrides{CoverSize: strPtr("huge")}, wantErr: "cover_size"},
		{name: "good cover size", o: &domain.DownloadOverrides{CoverSize: strPtr("3000x3000")}},
		{name: "bad alac sample rate", o: &domain.DownloadOverrides{ALACMaxSampleRate: intPtr(0)}, wantErr: "alac_max_sample_rate"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateDownloadOverrides(tt.o)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want contains %q", err, tt.wantErr)
			}
		})
	}
}
