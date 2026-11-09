package components

// SetProvider sets the active provider label shown in the panel.
func (s SidebarModel) SetProvider(label string) SidebarModel {
	s.provider = label
	return s
}

// clipRunes cuts s to n characters, ending with an ellipsis when it is cut.
func clipRunes(s string, n int) string {
	rs := []rune(s)
	if n < 1 || len(rs) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	return string(rs[:n-1]) + "…"
}
