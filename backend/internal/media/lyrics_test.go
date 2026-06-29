package media

import "testing"

func TestConvertLyricsToLRC(t *testing.T) {
	raw := `<tt><body><div><p begin="00:01.230">hello</p><p begin="00:02.000">world</p></div></body></tt>`
	got := convertLyrics(raw, "lrc")
	want := "[00:01.23]hello\n[00:02.00]world"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
