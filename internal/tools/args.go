package tools

import (
	"strconv"
	"strings"
)

// IntArg reads an integer argument. The model may send a JSON number
// (string="false") or text (string="true"), so both are accepted.
func IntArg(args map[string]any, name string) (int, bool) {
	switch v := args[name].(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		return n, err == nil
	}
	return 0, false
}

// BoolArg reads a boolean argument, accepting true/false as JSON or text.
func BoolArg(args map[string]any, name string) bool {
	switch v := args[name].(type) {
	case bool:
		return v
	case string:
		b, err := strconv.ParseBool(strings.TrimSpace(v))
		return err == nil && b
	}
	return false
}
