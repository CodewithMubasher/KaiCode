package components

import (
	"image/color"
	"strconv"
	"strings"
)

// Terminals have no real opacity, so "50% opacity" is done by blending every
// foreground and background colour halfway toward the page background.

// rgb8 converts any color.Color to 8-bit r, g, b.
func rgb8(c color.Color) (r, g, b int) {
	r16, g16, b16, _ := c.RGBA()
	return int(r16 >> 8), int(g16 >> 8), int(b16 >> 8)
}

// ansi16 is the xterm palette for colours 0-15.
var ansi16 = [16][3]int{
	{0, 0, 0}, {205, 0, 0}, {0, 205, 0}, {205, 205, 0},
	{0, 0, 238}, {205, 0, 205}, {0, 205, 205}, {229, 229, 229},
	{127, 127, 127}, {255, 0, 0}, {0, 255, 0}, {255, 255, 0},
	{92, 92, 255}, {255, 0, 255}, {0, 255, 255}, {255, 255, 255},
}

// ansi256 maps a 256-colour index to r, g, b.
func ansi256(n int) (int, int, int) {
	switch {
	case n < 0:
		n = 0
	case n > 255:
		n = 255
	}
	if n < 16 {
		c := ansi16[n]
		return c[0], c[1], c[2]
	}
	if n >= 232 {
		v := 8 + (n-232)*10
		return v, v, v
	}
	n -= 16
	cube := func(i int) int {
		if i == 0 {
			return 0
		}
		return 55 + i*40
	}
	return cube(n / 36), cube((n / 6) % 6), cube(n % 6)
}

// blend mixes (r,g,b) with the dim target; keep is how much of the original
// colour stays (0.5 = half).
func blend(r, g, b int, keep float64, tr, tg, tb int) (int, int, int) {
	mix := func(c, t int) int { return int(float64(c)*keep + float64(t)*(1-keep) + 0.5) }
	return mix(r, tr), mix(g, tg), mix(b, tb)
}

// DimFg returns the SGR sequence that sets the default text colour (ColText)
// dimmed by keep, for text that has no colour of its own.
func DimFg(keep float64) string {
	tr, tg, tb := rgb8(ColBG)
	r, g, b := rgb8(ColText)
	r, g, b = blend(r, g, b, keep, tr, tg, tb)
	return "\x1b[38;2;" + strconv.Itoa(r) + ";" + strconv.Itoa(g) + ";" + strconv.Itoa(b) + "m"
}

// Dim re-colours every ANSI colour in s so it appears at `keep` opacity
// (0.5 = 50%) over the page background. Text with no explicit colour is
// given a dimmed ColText so nothing is left at full brightness.
func Dim(s string, keep float64) string {
	tr, tg, tb := rgb8(ColBG)
	defFg := DimFg(keep)

	var out strings.Builder
	out.Grow(len(s) + len(s)/4)
	out.WriteString(defFg) // start of the string

	for i := 0; i < len(s); {
		// not a CSI sequence: copy through. Every line restarts with the dimmed
		// default colour, because Overlay cuts lines apart and each cut piece
		// must stand on its own.
		if s[i] != 0x1b || i+1 >= len(s) || s[i+1] != '[' {
			out.WriteByte(s[i])
			if s[i] == '\n' {
				out.WriteString(defFg)
			}
			i++
			continue
		}
		// find the end of the CSI sequence (final byte 0x40-0x7e)
		j := i + 2
		for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
			j++
		}
		if j >= len(s) { // truncated sequence, copy the rest
			out.WriteString(s[i:])
			break
		}
		seq := s[i : j+1]
		if s[j] != 'm' {
			out.WriteString(seq)
			i = j + 1
			continue
		}
		out.WriteString(dimSGR(s[i+2:j], keep, tr, tg, tb, defFg))
		i = j + 1
	}
	return out.String()
}

// dimSGR rewrites the parameters of one SGR sequence ("\x1b[" + params + "m").
// After a reset or a default-foreground it re-applies the dimmed default
// foreground so the next characters are not drawn at full brightness.
func dimSGR(params string, keep float64, tr, tg, tb int, defFg string) string {
	if strings.Contains(params, ":") { // sub-parameter form: leave untouched
		return "\x1b[" + params + "m"
	}
	parts := strings.Split(params, ";")
	var out []string
	needDefFg := false

	num := func(k int) int {
		if k >= len(parts) {
			return 0
		}
		n, _ := strconv.Atoi(parts[k])
		return n
	}
	setColor := func(prefix string, r, g, b int) {
		r, g, b = blend(r, g, b, keep, tr, tg, tb)
		out = append(out, prefix+";2;"+strconv.Itoa(r)+";"+strconv.Itoa(g)+";"+strconv.Itoa(b))
	}

	if params == "" { // "\x1b[m" is a reset
		return "\x1b[m" + defFg
	}
	for k := 0; k < len(parts); k++ {
		n := num(k)
		switch {
		case parts[k] == "" || n == 0:
			out = append(out, parts[k])
			needDefFg = true
		case n == 39:
			out = append(out, "39")
			needDefFg = true
		case n >= 30 && n <= 37:
			c := ansi16[n-30]
			setColor("38", c[0], c[1], c[2])
		case n >= 90 && n <= 97:
			c := ansi16[n-90+8]
			setColor("38", c[0], c[1], c[2])
		case n >= 40 && n <= 47:
			c := ansi16[n-40]
			setColor("48", c[0], c[1], c[2])
		case n >= 100 && n <= 107:
			c := ansi16[n-100+8]
			setColor("48", c[0], c[1], c[2])
		case n == 38 || n == 48 || n == 58:
			prefix := strconv.Itoa(n)
			mode := num(k + 1)
			switch {
			case mode == 5:
				r, g, b := ansi256(num(k + 2))
				if n == 58 {
					out = append(out, parts[k], parts[k+1], parts[min(k+2, len(parts)-1)])
				} else {
					setColor(prefix, r, g, b)
				}
				k += 2
			case mode == 2:
				if n == 58 {
					out = append(out, parts[k:min(k+5, len(parts))]...)
				} else {
					setColor(prefix, num(k+2), num(k+3), num(k+4))
				}
				k += 4
			default:
				out = append(out, parts[k])
			}
		default:
			out = append(out, parts[k])
		}
	}
	res := "\x1b[" + strings.Join(out, ";") + "m"
	if needDefFg {
		res += defFg
	}
	return res
}
