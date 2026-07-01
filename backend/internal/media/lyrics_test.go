package media

import (
	"strings"
	"testing"
)

func TestConvertLyricsToLRC(t *testing.T) {
	raw := `<tt><body><div><p begin="00:01.230">hello</p><p begin="00:02.000">world</p></div></body></tt>`
	got, err := convertLyrics(raw, "lrc", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "[00:01.23]hello\n[00:02.00]world"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestConvertLyricsKeepsTTMLFormat(t *testing.T) {
	raw := `<tt><body><div><p begin="00:01.230">hello</p></div></body></tt>`
	got, err := convertLyrics(raw, "ttml", []string{"translation"})
	if err != nil {
		t.Fatal(err)
	}
	if got != raw {
		t.Fatalf("got %q want raw TTML", got)
	}
}

func TestConvertLyricsTimeFormats(t *testing.T) {
	raw := `<tt><body><div>` +
		`<p begin="01:02:03.456">hour</p>` +
		`<p begin="02:03.400">minute</p>` +
		`<p begin="4.050">second</p>` +
		`</div></body></tt>`
	got, err := convertLyrics(raw, "lrc", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "[62:03.45]hour\n[02:03.40]minute\n[00:04.05]second"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestConvertLyricsTimingNoneOutputsPlainText(t *testing.T) {
	raw := `<tt itunes:timing="None"><body><div><p>first line</p><p>second line</p></div></body></tt>`
	got, err := convertLyrics(raw, "lrc", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "first line\nsecond line"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestConvertLyricsWordTimingOutputsEnhancedLRC(t *testing.T) {
	raw := `<tt itunes:timing="Word"><body><div><p itunes:key="L1">` +
		`<span begin="00:01.000" end="00:01.500">Hello</span> ` +
		`<span begin="00:01.600" end="00:02.000">world</span>` +
		`</p></div></body></tt>`
	got, err := convertLyrics(raw, "lrc", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "[00:01.00]<00:01.00>Hello <00:01.60>world<00:02.00>"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestConvertLyricsUsesTextAttributesNestedSpansAndEscapes(t *testing.T) {
	raw := `<tt><body><div><p begin="00:01.000" text="from attr"></p><p begin="00:02.000">hello <span>wide</span> &amp; clear</p></div></body></tt>`
	got, err := convertLyrics(raw, "lrc", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "[00:01.00]from attr\n[00:02.00]hello wide & clear"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestConvertLyricsExtrasAppendTranslationAndPronunciation(t *testing.T) {
	raw := `<tt><head><metadata><iTunesMetadata>` +
		`<translations><translation><text for="L1" text="translated"></text></translation></translations>` +
		`<transliterations><transliteration><text for="L1" text="hakuna"></text></transliteration></transliterations>` +
		`</iTunesMetadata></metadata></head><body><div><p begin="00:01.000" itunes:key="L1">歌</p></div></body></tt>`
	got, err := convertLyrics(raw, "lrc", []string{"translation", "pronunciation"})
	if err != nil {
		t.Fatal(err)
	}
	want := "[00:01.00]歌\n[00:01.00]translated\n[00:01.00]hakuna"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestConvertLyricsWordTimingExtras(t *testing.T) {
	raw := `<tt itunes:timing="Word"><head><metadata><iTunesMetadata>` +
		`<translations><translation><text for="L1" text="translated"></text></translation></translations>` +
		`<transliterations><transliteration><text for="L1"><span begin="00:01.000">ha</span><span begin="00:01.400">ku</span></text></transliteration></transliterations>` +
		`</iTunesMetadata></metadata></head><body><div><p itunes:key="L1">` +
		`<span begin="00:01.000" end="00:01.500">歌</span>` +
		`</p></div></body></tt>`
	got, err := convertLyrics(raw, "lrc", []string{"translation", "pronunciation"})
	if err != nil {
		t.Fatal(err)
	}
	want := "[00:01.00]<00:01.00>歌<00:01.50>\n[00:01.00]translated\n[00:01.00]<00:01.00>ha <00:01.40>ku"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestConvertLyricsMalformedTTMLReturnsError(t *testing.T) {
	_, err := convertLyrics(`<tt><body>`, "lrc", nil)
	if err == nil {
		t.Fatal("expected malformed TTML error")
	}
	if !strings.Contains(err.Error(), "lyrics") {
		t.Fatalf("error = %v, want lyrics context", err)
	}
}
