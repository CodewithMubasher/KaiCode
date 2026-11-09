package components

import (
"strconv"
"strings"

"charm.land/lipgloss/v2"
)

func shellLines(cmd, output string, w int) []string {
const lead, tail = 2, 2
pw := w - 1
inner := pw - lead - tail
if inner < 1 {
inner = 1
}

bg := lipgloss.NewStyle().Background(ColShellBg)

// Check if this is a file block
isFile := strings.HasPrefix(cmd, "Created ") || strings.HasPrefix(cmd, "Wrote ")

cmdStyle := bg.Foreground(ColText).Bold(true)
if isFile {
cmdStyle = bg.Foreground(ColTeal).Bold(true) // Teal heading for files
}

outStyle := bg.Foreground(ColDim)
fileStyle := bg.Foreground(ColText)
numStyle := bg.Foreground(ColMuted)

row := func(text string, st lipgloss.Style) string {
return bg.Render(spaces(lead)) + st.Render(padRight(text, inner)) + bg.Render(spaces(tail))
}
blank := bg.Render(spaces(pw))

out := []string{blank}
for _, l := range wrapText("$ "+cmd, inner) {
out = append(out, row(strings.TrimRight(l, " "), cmdStyle))
}
if output != "" {
out = append(out, blank)
output = strings.ReplaceAll(strings.TrimRight(output, "\n"), "\t", "    ")
lines := strings.Split(output, "\n")

numW := 1
if isFile {
numW = len(strconv.Itoa(len(lines)))
}

for i, line := range lines {
if isFile {
num := strconv.Itoa(i + 1)
numPad := spaces(numW - len(num))
numStr := numStyle.Render(numPad + num)

for n, l := range hardWrap(line, inner-numW-2) {
if n == 0 {
out = append(out, bg.Render(spaces(lead)) + numStr + bg.Render(" ") + fileStyle.Render(padRight(l, inner-numW-1)) + bg.Render(spaces(tail)))
} else {
out = append(out, bg.Render(spaces(lead)) + bg.Render(spaces(numW+1)) + fileStyle.Render(padRight(l, inner-numW-1)) + bg.Render(spaces(tail)))
}
}
} else {
for _, l := range hardWrap(line, inner) {
out = append(out, row(l, outStyle))
}
}
}
}
return withBar(append(out, blank), ColShellBg)
}
