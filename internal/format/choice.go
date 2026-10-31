package format

import "slices"

// QualityChoices are the qualities a download of a hit with audio traits
// asks between: lossless and Dolby, when the hit has both and the server's
// quality priority lists both, in the priority's order. Any other mix, AAC
// among them, the server settles on its own: nil asks nothing.
func QualityChoices(traits, priority []string) []string {
	lossless := slices.Contains(traits, "lossless") || slices.Contains(traits, "hi-res-lossless")
	dolby := slices.Contains(traits, "atmos")
	var out []string
	hasDolby := false
	for _, c := range priority {
		switch {
		case c == "alac" && lossless && !slices.Contains(out, c):
			out = append(out, c)
		case (c == "ec3" || c == "ac3") && dolby && !hasDolby:
			out, hasDolby = append(out, c), true
		}
	}
	if len(out) < 2 {
		return nil
	}
	return out
}

// CodecName names a codec a download offers.
func CodecName(c string) string {
	switch c {
	case "alac":
		return "Lossless"
	case "ec3":
		return "Dolby Atmos"
	case "ac3":
		return "Dolby Digital"
	}
	return c
}
