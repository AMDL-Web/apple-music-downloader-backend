package media

import (
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
)

type lyricNode struct {
	tag      string
	attrs    map[string]string
	content  []lyricContent
	children []*lyricNode
}

type lyricContent struct {
	text string
	node *lyricNode
}

func convertLyrics(ttml, format string, extras []string) (string, error) {
	if strings.EqualFold(format, "ttml") {
		return ttml, nil
	}
	root, err := parseLyricsXML(ttml)
	if err != nil {
		return "", fmt.Errorf("parse lyrics TTML: %w", err)
	}
	if root == nil {
		return "", fmt.Errorf("parse lyrics TTML: empty document")
	}
	switch strings.ToLower(attr(root, "itunes:timing", "timing")) {
	case "none":
		return plainTextLyrics(root), nil
	case "word":
		return wordTimedLyrics(root, extras)
	default:
		return lineTimedLyrics(root, extras)
	}
}

func parseLyricsXML(raw string) (*lyricNode, error) {
	decoder := xml.NewDecoder(strings.NewReader(raw))
	var stack []*lyricNode
	var root *lyricNode
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch tok := token.(type) {
		case xml.StartElement:
			node := &lyricNode{tag: tok.Name.Local, attrs: map[string]string{}}
			for _, xmlAttr := range tok.Attr {
				name := xmlAttr.Name.Local
				if xmlAttr.Name.Space != "" {
					name = xmlAttr.Name.Space + ":" + xmlAttr.Name.Local
				}
				node.attrs[name] = xmlAttr.Value
			}
			if len(stack) == 0 {
				root = node
			} else {
				parent := stack[len(stack)-1]
				parent.children = append(parent.children, node)
				parent.content = append(parent.content, lyricContent{node: node})
			}
			stack = append(stack, node)
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].content = append(stack[len(stack)-1].content, lyricContent{text: string(tok)})
			}
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	if len(stack) != 0 {
		return nil, fmt.Errorf("unclosed XML elements")
	}
	return root, nil
}

func plainTextLyrics(root *lyricNode) string {
	var lines []string
	for _, p := range findNodes(root, "p") {
		text := strings.TrimSpace(nodeText(p))
		if text != "" {
			lines = append(lines, text)
		}
	}
	return strings.Join(lines, "\n")
}

func lineTimedLyrics(root *lyricNode, extras []string) (string, error) {
	localizations := localizationMaps(root)
	var lines []string
	for _, p := range lyricParagraphs(root) {
		begin := attr(p, "begin")
		if begin == "" {
			return "", fmt.Errorf("no synchronised lyrics")
		}
		timestamp, err := lrcTime(begin)
		if err != nil {
			return "", err
		}
		text := strings.TrimSpace(preferredNodeText(p))
		if text == "" {
			continue
		}
		lines = append(lines, "["+timestamp+"]"+text)
		key := attr(p, "itunes:key", "key")
		lines = appendLineExtras(lines, "["+timestamp+"]", key, text, localizations, extras, false)
	}
	return strings.Join(lines, "\n"), nil
}

func wordTimedLyrics(root *lyricNode, extras []string) (string, error) {
	localizations := localizationMaps(root)
	var lines []string
	for _, p := range lyricParagraphs(root) {
		text, start, containsCJKText, err := enhancedLine(p)
		if err != nil {
			return "", err
		}
		if text == "" {
			continue
		}
		lines = append(lines, text)
		key := attr(p, "itunes:key", "key")
		lines = appendLineExtras(lines, start, key, nodeText(p), localizations, extras, containsCJKText)
	}
	return strings.Join(lines, "\n"), nil
}

func enhancedLine(p *lyricNode) (line string, startPrefix string, hasCJK bool, err error) {
	var pieces []string
	startPrefix = ""
	endSuffix := ""
	textForCJK := ""
	for _, content := range p.content {
		child := content.node
		if child == nil {
			if len(pieces) > 0 && strings.TrimSpace(content.text) == "" && !strings.Contains(content.text, "\n") {
				pieces = append(pieces, content.text)
			}
			continue
		}
		if child.tag != "span" {
			continue
		}
		begin := attr(child, "begin")
		if begin == "" {
			continue
		}
		beginTime, err := lrcTime(begin)
		if err != nil {
			return "", "", false, err
		}
		if startPrefix == "" {
			startPrefix = "[" + beginTime + "]"
			pieces = append(pieces, startPrefix)
		}
		pieces = append(pieces, "<"+beginTime+">"+preferredNodeText(child))
		textForCJK += preferredNodeText(child)
		if end := attr(child, "end"); end != "" {
			endTime, err := lrcTime(end)
			if err != nil {
				return "", "", false, err
			}
			endSuffix = "<" + endTime + ">"
		}
	}
	if len(pieces) == 0 {
		return "", "", false, nil
	}
	return strings.Join(pieces, "") + endSuffix, startPrefix, containsCJK(textForCJK), nil
}

func appendLineExtras(lines []string, prefix, key, sourceText string, localizations lyricLocalizations, extras []string, alreadyCheckedCJK bool) []string {
	if key == "" || prefix == "" {
		return lines
	}
	extraSet := stringSet(extras)
	if _, ok := extraSet["translation"]; ok {
		if text := localizations.translations[key].plain(prefix); text != "" {
			lines = append(lines, text)
		}
	}
	if _, ok := extraSet["pronunciation"]; ok {
		hasCJK := alreadyCheckedCJK
		if !hasCJK {
			hasCJK = containsCJK(sourceText)
		}
		if hasCJK {
			if text := localizations.transliterations[key].plain(prefix); text != "" {
				lines = append(lines, text)
			}
		}
	}
	return lines
}

type lyricLocalizations struct {
	translations     map[string]localizedText
	transliterations map[string]localizedText
}

type localizedText struct {
	text  string
	spans []localizedSpan
}

type localizedSpan struct {
	begin string
	text  string
}

func (l localizedText) plain(prefix string) string {
	if l.text != "" {
		return prefix + l.text
	}
	if len(l.spans) == 0 {
		return ""
	}
	var parts []string
	for _, span := range l.spans {
		if span.begin == "" || span.text == "" {
			continue
		}
		ts, err := lrcTime(span.begin)
		if err != nil {
			continue
		}
		parts = append(parts, "<"+ts+">"+span.text)
	}
	if len(parts) == 0 {
		return ""
	}
	return prefix + strings.Join(parts, " ")
}

func localizationMaps(root *lyricNode) lyricLocalizations {
	localizations := lyricLocalizations{
		translations:     map[string]localizedText{},
		transliterations: map[string]localizedText{},
	}
	for _, translations := range findNodes(root, "translations") {
		for _, textNode := range findNodes(translations, "text") {
			if key := attr(textNode, "for"); key != "" {
				localizations.translations[key] = readLocalizedText(textNode)
			}
		}
	}
	for _, transliterations := range findNodes(root, "transliterations") {
		for _, textNode := range findNodes(transliterations, "text") {
			if key := attr(textNode, "for"); key != "" {
				localizations.transliterations[key] = readLocalizedText(textNode)
			}
		}
	}
	return localizations
}

func readLocalizedText(node *lyricNode) localizedText {
	if text := strings.TrimSpace(attr(node, "text")); text != "" {
		return localizedText{text: text}
	}
	var spans []localizedSpan
	for _, child := range node.children {
		if child.tag != "span" {
			continue
		}
		spans = append(spans, localizedSpan{begin: attr(child, "begin"), text: strings.TrimSpace(preferredNodeText(child))})
	}
	if len(spans) > 0 {
		return localizedText{spans: spans}
	}
	return localizedText{text: strings.TrimSpace(nodeText(node))}
}

func lyricParagraphs(root *lyricNode) []*lyricNode {
	var out []*lyricNode
	var walk func(*lyricNode, bool)
	walk = func(node *lyricNode, inBody bool) {
		if node == nil {
			return
		}
		if node.tag == "body" {
			inBody = true
		}
		if inBody && node.tag == "p" {
			out = append(out, node)
			return
		}
		for _, child := range node.children {
			walk(child, inBody)
		}
	}
	walk(root, false)
	return out
}

func findNodes(root *lyricNode, tag string) []*lyricNode {
	var out []*lyricNode
	var walk func(*lyricNode)
	walk = func(node *lyricNode) {
		if node == nil {
			return
		}
		if node.tag == tag {
			out = append(out, node)
		}
		for _, child := range node.children {
			walk(child)
		}
	}
	walk(root)
	return out
}

func preferredNodeText(node *lyricNode) string {
	if text := attr(node, "text"); text != "" {
		return text
	}
	return nodeText(node)
}

func nodeText(node *lyricNode) string {
	var b strings.Builder
	var write func(*lyricNode)
	write = func(n *lyricNode) {
		if n == nil {
			return
		}
		for _, content := range n.content {
			if content.node != nil {
				write(content.node)
			} else {
				b.WriteString(content.text)
			}
		}
	}
	write(node)
	return strings.TrimSpace(b.String())
}

func attr(node *lyricNode, names ...string) string {
	if node == nil {
		return ""
	}
	for _, name := range names {
		if value, ok := node.attrs[name]; ok {
			return value
		}
	}
	return ""
}

func lrcTime(v string) (string, error) {
	if !strings.Contains(v, ".") {
		v += ".000"
	}
	parts := strings.Split(v, ":")
	h, m := 0, 0
	secPart := parts[len(parts)-1]
	if len(parts) == 3 {
		var err error
		h, err = strconv.Atoi(parts[0])
		if err != nil {
			return "", err
		}
		m, err = strconv.Atoi(parts[1])
		if err != nil {
			return "", err
		}
	} else if len(parts) == 2 {
		var err error
		m, err = strconv.Atoi(parts[0])
		if err != nil {
			return "", err
		}
	} else if len(parts) > 3 {
		return "", fmt.Errorf("invalid lyrics timestamp %q", v)
	}
	sm := strings.SplitN(secPart, ".", 2)
	s, err := strconv.Atoi(sm[0])
	if err != nil {
		return "", err
	}
	ms := 0
	if len(sm) == 2 {
		ms, err = strconv.Atoi(rightPad(sm[1], 3)[:3])
		if err != nil {
			return "", err
		}
	}
	totalM := h*60 + m
	return left2(totalM) + ":" + left2(s) + "." + left2(ms/10), nil
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
	if len(s) > n {
		return s[:n]
	}
	return s
}

func stringSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

func containsCJK(s string) bool {
	for _, r := range s {
		if (r >= 0x1100 && r <= 0x11FF) ||
			(r >= 0x2E80 && r <= 0x2EFF) ||
			(r >= 0x2F00 && r <= 0x2FDF) ||
			(r >= 0x2FF0 && r <= 0x2FFF) ||
			(r >= 0x3000 && r <= 0x303F) ||
			(r >= 0x3040 && r <= 0x309F) ||
			(r >= 0x30A0 && r <= 0x30FF) ||
			(r >= 0x3130 && r <= 0x318F) ||
			(r >= 0x31C0 && r <= 0x31EF) ||
			(r >= 0x31F0 && r <= 0x31FF) ||
			(r >= 0x3200 && r <= 0x32FF) ||
			(r >= 0x3300 && r <= 0x33FF) ||
			(r >= 0x3400 && r <= 0x4DBF) ||
			(r >= 0x4E00 && r <= 0x9FFF) ||
			(r >= 0xA960 && r <= 0xA97F) ||
			(r >= 0xAC00 && r <= 0xD7AF) ||
			(r >= 0xD7B0 && r <= 0xD7FF) ||
			(r >= 0xF900 && r <= 0xFAFF) ||
			(r >= 0xFE30 && r <= 0xFE4F) ||
			(r >= 0xFF65 && r <= 0xFF9F) ||
			(r >= 0xFFA0 && r <= 0xFFDC) ||
			(r >= 0x1AFF0 && r <= 0x1AFFF) ||
			(r >= 0x1B000 && r <= 0x1B0FF) ||
			(r >= 0x1B100 && r <= 0x1B12F) ||
			(r >= 0x1B130 && r <= 0x1B16F) ||
			(r >= 0x1F200 && r <= 0x1F2FF) ||
			(r >= 0x20000 && r <= 0x2A6DF) ||
			(r >= 0x2A700 && r <= 0x2B73F) ||
			(r >= 0x2B740 && r <= 0x2B81F) ||
			(r >= 0x2B820 && r <= 0x2CEAF) ||
			(r >= 0x2CEB0 && r <= 0x2EBEF) ||
			(r >= 0x2EBF0 && r <= 0x2EE5F) ||
			(r >= 0x2F800 && r <= 0x2FA1F) ||
			(r >= 0x30000 && r <= 0x3134F) ||
			(r >= 0x31350 && r <= 0x323AF) {
			return true
		}
	}
	return false
}
