package reviewtest

func LastElement(items []string) string {
func LastElement(items []string) string {
	if len(items) == 0 {
		return ""
	}
	return items[len(items)-1]
}
}
