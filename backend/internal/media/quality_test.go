package media

import "testing"

func TestSummarizeQualitiesFromMasterPlaylist(t *testing.T) {
	const master = `#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio-stereo-256",NAME="AAC",URI="aac/prog.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=281000,AVERAGE-BANDWIDTH=256000,CODECS="mp4a.40.2",AUDIO="audio-stereo-256"
aac/master.m3u8
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio-alac-stereo-96000-24",NAME="ALAC",URI="alac/prog.m3u8",BIT-DEPTH=24,SAMPLE-RATE=96000
#EXT-X-STREAM-INF:BANDWIDTH=4600000,AVERAGE-BANDWIDTH=3900000,CODECS="alac",AUDIO="audio-alac-stereo-96000-24"
alac/master.m3u8
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio-atmos-2448",NAME="Atmos",URI="atmos/prog.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=768000,AVERAGE-BANDWIDTH=640000,CODECS="ec-3",AUDIO="audio-atmos-2448"
atmos/master.m3u8
`

	variants := ParseMasterPlaylist(master, "https://example.test/hls/master.m3u8")
	qualities := SummarizeQualities(variants)

	want := map[string]QualityOption{
		"aac":  {Available: true, CodecID: "audio-stereo-256", Bitrate: 256000},
		"alac": {Available: true, CodecID: "audio-alac-stereo-96000-24", Bitrate: 3900000, BitDepth: 24, SampleRate: 96000},
		"ec3":  {Available: true, CodecID: "audio-atmos-2448", Bitrate: 640000},
	}
	got := map[string]QualityOption{}
	for _, quality := range qualities {
		got[quality.ID] = quality
	}
	for id, expected := range want {
		actual, ok := got[id]
		if !ok {
			t.Fatalf("quality %q missing from %#v", id, qualities)
		}
		if !actual.Available || actual.CodecID != expected.CodecID || actual.Bitrate != expected.Bitrate || actual.BitDepth != expected.BitDepth || actual.SampleRate != expected.SampleRate {
			t.Fatalf("quality %q = %#v, want fields %#v", id, actual, expected)
		}
	}
	if got["alac"].Description == "" {
		t.Fatalf("ALAC description is empty")
	}
}
