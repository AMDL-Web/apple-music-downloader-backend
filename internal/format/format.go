// Package format writes the numbers the amdl clients show people: sizes,
// durations, rates and audio quality labels, and which qualities a download
// asks between.
package format

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Bytes writes a size in decimal units, as Finder does: "41.0 MB".
func Bytes(n int64) string {
	v, unit := Scale(float64(n))
	if unit == "B" {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f %s", v, unit)
}

// Scale returns n bytes in the largest decimal unit that keeps the value at
// least 1, for a big number set apart from its unit.
func Scale(n float64) (float64, string) {
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	i := 0
	for n >= 1000 && i < len(units)-1 {
		n /= 1000
		i++
	}
	return n, units[i]
}

// Number writes v with at most digits decimals, without trailing zeros:
// 1.62, 560, 7.7.
func Number(v float64, digits int) string {
	s := strconv.FormatFloat(v, 'f', digits, 64)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}

// Rate writes bytes per second: "41 MB/s".
func Rate(bps float64) string {
	v, unit := Scale(bps)
	if v >= 100 || unit == "B" {
		return fmt.Sprintf("%.0f %s/s", v, unit)
	}
	return fmt.Sprintf("%s %s/s", Number(v, 1), unit)
}

// Remaining writes an estimate of time left: "~58 s", "~4 min", "~2 h".
func Remaining(d time.Duration) string {
	switch {
	case d <= 0:
		return ""
	case d < time.Minute:
		return fmt.Sprintf("~%d s", int(d.Seconds()+0.5))
	case d < time.Hour:
		return fmt.Sprintf("~%d min", int(d.Minutes()+0.5))
	}
	return fmt.Sprintf("~%s h", Number(d.Hours(), 1))
}

// Duration writes a track length: "3:20", "1:02:05".
func Duration(d time.Duration) string {
	s := int(d.Round(time.Second).Seconds())
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// Quality writes a track's codec and resolution as the app labels it:
// "ALAC 24/48", "ALAC 16/44.1", "Atmos", "AAC 256".
func Quality(codec string, bitDepth, sampleRate, bitrate int) string {
	switch strings.ToLower(codec) {
	case "":
		return ""
	case "alac":
		if bitDepth > 0 && sampleRate > 0 {
			return fmt.Sprintf("ALAC %d/%s", bitDepth, Number(float64(sampleRate)/1000, 1))
		}
		return "ALAC"
	case "ec3":
		return "Atmos"
	case "ac3":
		return "Dolby Digital"
	case "aac-binaural":
		return "AAC Binaural"
	case "aac-downmix":
		return "AAC Downmix"
	}
	label := strings.ToUpper(codec)
	if bitrate > 0 {
		label += " " + strconv.Itoa(bitrate/1000)
	}
	return label
}

// LongQuality writes the detail screen's longer label: "ALAC 24-bit · 48 kHz".
func LongQuality(codec string, bitDepth, sampleRate int) string {
	if strings.EqualFold(codec, "alac") && bitDepth > 0 && sampleRate > 0 {
		return fmt.Sprintf("ALAC %d-bit · %s kHz", bitDepth, Number(float64(sampleRate)/1000, 1))
	}
	return Quality(codec, bitDepth, sampleRate, 0)
}

// Clock writes a time of day in 24-hour form: "10:41".
func Clock(t time.Time) string { return t.Local().Format("15:04") }

// Seconds writes a time of day with seconds: "10:41:09".
func Seconds(t time.Time) string { return t.Local().Format("15:04:05") }
