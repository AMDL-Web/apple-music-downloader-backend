package media

import "testing"

const mvMaster = `#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio-stereo-64",NAME="English",URI="audio/audio_en_gr64.m3u8"
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio-stereo-128",NAME="English",URI="audio/audio_en_gr128.m3u8"
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio-stereo-256",NAME="English",URI="audio/audio_en_gr256.m3u8"
#EXT-X-I-FRAME-STREAM-INF:BANDWIDTH=187734,RESOLUTION=672x378,URI="iframe/trick.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=834582,AVERAGE-BANDWIDTH=561833,RESOLUTION=608x342,AUDIO="audio-stereo-64",ALLOWED-CPC="com.apple.streamingkeydelivery:Baseline/AppleBaseline,urn:uuid:edef8ba9-79d6-4ace-a3c8-27dcd51d21ed:WIDEVINE_SOFTWARE"
video/video_608x342.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=2460551,AVERAGE-BANDWIDTH=1622760,RESOLUTION=864x486,AUDIO="audio-stereo-128",ALLOWED-CPC="com.apple.streamingkeydelivery:Baseline/AppleBaseline,urn:uuid:edef8ba9-79d6-4ace-a3c8-27dcd51d21ed:WIDEVINE_SOFTWARE"
video/video_864x486.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=7634705,AVERAGE-BANDWIDTH=5242686,RESOLUTION=1920x1080,AUDIO="audio-stereo-256",ALLOWED-CPC="com.apple.streamingkeydelivery:Baseline/AppleBaseline,urn:uuid:edef8ba9-79d6-4ace-a3c8-27dcd51d21ed:WIDEVINE_HARDWARE"
video/video_1920x1080.m3u8
`

func TestSelectMVStreamsPrefersSoftwareWithinHeight(t *testing.T) {
	base := "https://example.com/hls/master.m3u8"
	video, audioURL, err := selectMVStreams(mvMaster, base, 2160, "atmos")
	if err != nil {
		t.Fatalf("selectMVStreams: %v", err)
	}
	// 1080p requires hardware -> excluded; best software is 864x486.
	if video.Height != 486 {
		t.Fatalf("expected 486 software height, got %d (%s, cpc=%s)", video.Height, video.Resolution, video.AllowedCPC)
	}
	if video.URI != "https://example.com/hls/video/video_864x486.m3u8" {
		t.Fatalf("unexpected video URI %q", video.URI)
	}
	if audioURL != "https://example.com/hls/audio/audio_en_gr128.m3u8" {
		t.Fatalf("unexpected audio URI %q", audioURL)
	}
}

func TestSelectMVStreamsRespectsMaxHeight(t *testing.T) {
	base := "https://example.com/hls/master.m3u8"
	video, _, err := selectMVStreams(mvMaster, base, 360, "aac")
	if err != nil {
		t.Fatalf("selectMVStreams: %v", err)
	}
	// nothing <=360, falls back to shortest software variant (608x342).
	if video.Height != 342 {
		t.Fatalf("expected 342 fallback height, got %d", video.Height)
	}
}

func TestParseMVMediaWidevineKey(t *testing.T) {
	body := `#EXTM3U
#EXT-X-VERSION:7
#EXT-X-KEY:METHOD=SAMPLE-AES,URI="skd://itunes.apple.com/p1/c0",KEYFORMAT="com.apple.streamingkeydelivery",KEYFORMATVERSIONS="1"
#EXT-X-KEY:METHOD=SAMPLE-AES,URI="data:text/plain;charset=UTF-16;base64,UExBWVJFQURZ",KEYFORMAT="com.microsoft.playready",KEYFORMATVERSIONS="1"
#EXT-X-KEY:METHOD=SAMPLE-AES,URI="data:text/plain;base64,AAAAOHBzc2gAAAAA7e+LqXnWSs6jyCfc1R0h7QAAABg",KEYFORMAT="urn:uuid:edef8ba9-79d6-4ace-a3c8-27dcd51d21ed",KEYFORMATVERSIONS="1"
#EXT-X-MAP:URI="init.mp4"
#EXTINF:6.0,
seg1.m4s
#EXTINF:6.0,
seg2.m4s
#EXT-X-ENDLIST
`
	base := "https://example.com/hls/video/prog.m3u8"
	media, err := parseMVMedia(body, base)
	if err != nil {
		t.Fatalf("parseMVMedia: %v", err)
	}
	if media.PSSHB64 != "AAAAOHBzc2gAAAAA7e+LqXnWSs6jyCfc1R0h7QAAABg" {
		t.Fatalf("unexpected PSSH %q", media.PSSHB64)
	}
	if media.InitURL != "https://example.com/hls/video/init.mp4" {
		t.Fatalf("unexpected init URL %q", media.InitURL)
	}
	if len(media.Segments) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(media.Segments))
	}
}
