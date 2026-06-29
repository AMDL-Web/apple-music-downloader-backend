package media

import (
	"html"
	"regexp"
	"strconv"
	"strings"
)

var (
	beginAttr = regexp.MustCompile(`begin="([^"]+)"`)
	pBlock    = regexp.MustCompile(`(?s)<p\b[^>]*>.*?</p>`)
	tagRe     = regexp.MustCompile(`<[^>]+>`)
)

func convertLyrics(ttml, format string) string {
	if strings.EqualFold(format, "ttml") {
		return ttml
	}
	var lines []string
	for _, block := range pBlock.FindAllString(ttml, -1) {
		begin := ""
		if m := beginAttr.FindStringSubmatch(block); len(m) == 2 {
			begin = m[1]
		}
		if begin == "" {
			continue
		}
		text := html.UnescapeString(strings.TrimSpace(tagRe.ReplaceAllString(block, "")))
		if text == "" {
			continue
		}
		lines = append(lines, "["+lrcTime(begin)+"]"+text)
	}
	return strings.Join(lines, "\n")
}

func lrcTime(v string) string {
	if !strings.Contains(v, ".") {
		v += ".000"
	}
	parts := strings.Split(v, ":")
	h, m := 0, 0
	secPart := parts[len(parts)-1]
	if len(parts) == 3 {
		h = atoi(parts[0])
		m = atoi(parts[1])
	} else if len(parts) == 2 {
		m = atoi(parts[0])
	}
	sm := strings.SplitN(secPart, ".", 2)
	s := atoi(sm[0])
	ms := 0
	if len(sm) == 2 {
		ms = atoi(rightPad(sm[1], 3)[:3])
	}
	totalM := h*60 + m
	return left2(totalM) + ":" + left2(s) + "." + left2(ms/10)
}

func left2(i int) string {
	if i < 10 {
		return "0" + strconv.Itoa(i)
	}
	return strconv.Itoa(i)
}

func rightPad(s string, n int) string {
	for len(s) < n {
		s += "0"
	}
	return s
}
