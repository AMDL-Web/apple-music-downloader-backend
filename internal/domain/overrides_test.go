package domain

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// fullOverrides builds an overlay with every field set to a sentinel derived
// from seed, via reflection, so tests fail when a new field is added to the
// struct but forgotten in Merge (or any other per-field enumeration).
func fullOverrides(t *testing.T, seed int) *DownloadOverrides {
	t.Helper()
	var o DownloadOverrides
	v := reflect.ValueOf(&o).Elem()
	for i := 0; i < v.NumField(); i++ {
		field := v.Type().Field(i)
		elem := reflect.New(field.Type.Elem())
		switch elem.Elem().Kind() {
		case reflect.String:
			elem.Elem().SetString(fmt.Sprintf("s%d-%s", seed, field.Name))
		case reflect.Bool:
			elem.Elem().SetBool(seed%2 == 0)
		case reflect.Int:
			elem.Elem().SetInt(int64(seed))
		case reflect.Slice:
			slice := reflect.MakeSlice(field.Type.Elem(), 1, 1)
			slice.Index(0).SetString(fmt.Sprintf("s%d", seed))
			elem.Elem().Set(slice)
		default:
			t.Fatalf("field %s has unhandled kind %s; extend fullOverrides", field.Name, elem.Elem().Kind())
		}
		v.Field(i).Set(elem)
	}
	return &o
}

// TestMergeDownloadOverridesCoversEveryField guards the hand-written
// per-field Merge against silently dropping a newly added struct field.
func TestMergeDownloadOverridesCoversEveryField(t *testing.T) {
	lower := fullOverrides(t, 1)
	upper := fullOverrides(t, 2)

	merged := MergeDownloadOverrides(lower, upper)
	if merged == nil {
		t.Fatal("merged = nil")
	}
	mv := reflect.ValueOf(*merged)
	uv := reflect.ValueOf(*upper)
	for i := 0; i < mv.NumField(); i++ {
		name := mv.Type().Field(i).Name
		if mv.Field(i).IsNil() {
			t.Errorf("MergeDownloadOverrides drops field %s", name)
			continue
		}
		if !reflect.DeepEqual(mv.Field(i).Elem().Interface(), uv.Field(i).Elem().Interface()) {
			t.Errorf("field %s = %v, want later layer's %v", name, mv.Field(i).Elem(), uv.Field(i).Elem())
		}
	}
}

func TestDownloadOverrideKeysMatchStruct(t *testing.T) {
	keys := DownloadOverrideKeys()
	typ := reflect.TypeOf(DownloadOverrides{})
	if len(keys) != typ.NumField() {
		t.Fatalf("keys = %d, struct fields = %d", len(keys), typ.NumField())
	}
	for _, key := range keys {
		if key == "" || key == "-" || strings.Contains(key, ",") {
			t.Fatalf("malformed key %q", key)
		}
	}
}

func TestParseDownloadOverridesEmptyInputs(t *testing.T) {
	for _, raw := range []string{"", "  ", "null", "{}"} {
		o, err := ParseDownloadOverrides([]byte(raw))
		if err != nil {
			t.Fatalf("ParseDownloadOverrides(%q) error: %v", raw, err)
		}
		if o != nil {
			t.Fatalf("ParseDownloadOverrides(%q) = %+v, want nil", raw, o)
		}
	}
}

func TestParseDownloadOverridesRejectsUnknownFields(t *testing.T) {
	_, err := ParseDownloadOverrides([]byte(`{"embed_lyric": false}`))
	if err == nil || !strings.Contains(err.Error(), "embed_lyric") {
		t.Fatalf("err = %v, want unknown field error", err)
	}
}

func TestParseDownloadOverridesParsesFields(t *testing.T) {
	o, err := ParseDownloadOverrides([]byte(`{"quality_priority":["aac"],"embed_lyrics":false,"retries":5}`))
	if err != nil {
		t.Fatal(err)
	}
	if o == nil || o.QualityPriority == nil || len(*o.QualityPriority) != 1 || (*o.QualityPriority)[0] != "aac" {
		t.Fatalf("quality_priority = %+v", o)
	}
	if o.EmbedLyrics == nil || *o.EmbedLyrics {
		t.Fatalf("embed_lyrics = %+v, want false", o.EmbedLyrics)
	}
	if o.Retries == nil || *o.Retries != 5 {
		t.Fatalf("retries = %+v, want 5", o.Retries)
	}
	if o.CoverFormat != nil {
		t.Fatalf("cover_format = %+v, want nil (not set)", o.CoverFormat)
	}
}

func TestMergeDownloadOverridesLaterLayersWin(t *testing.T) {
	trueVal, falseVal := true, false
	userFormat := "user-{SongName}"
	user := &DownloadOverrides{EmbedLyrics: &trueVal, SongFileFormat: &userFormat}
	request := &DownloadOverrides{EmbedLyrics: &falseVal}

	merged := MergeDownloadOverrides(user, request)
	if merged == nil {
		t.Fatal("merged = nil")
	}
	if merged.EmbedLyrics == nil || *merged.EmbedLyrics {
		t.Fatal("request layer should win for embed_lyrics")
	}
	if merged.SongFileFormat == nil || *merged.SongFileFormat != userFormat {
		t.Fatal("user layer should survive for fields the request does not set")
	}
}

func TestMergeDownloadOverridesNilAndEmpty(t *testing.T) {
	if merged := MergeDownloadOverrides(nil, nil); merged != nil {
		t.Fatalf("merged = %+v, want nil", merged)
	}
	if merged := MergeDownloadOverrides(nil, &DownloadOverrides{}); merged != nil {
		t.Fatalf("merged = %+v, want nil for empty layers", merged)
	}
	v := 2
	if merged := MergeDownloadOverrides(nil, &DownloadOverrides{Retries: &v}, nil); merged == nil || merged.Retries == nil || *merged.Retries != 2 {
		t.Fatalf("merged = %+v, want retries 2", merged)
	}
}
