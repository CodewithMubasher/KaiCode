// Package deepseek decodes the server-sent-event stream of DeepSeek's web
// chat into harness events.
package deepseek

import (
	"bufio"
	"io"
	"strings"
)

// ReadSSE reads a server-sent-event stream and calls fn once per event with
// its "event:" name (empty if none) and its joined "data:" lines. Lines are
// handed over as soon as they arrive, so streaming is not delayed.
func ReadSSE(r io.Reader, fn func(event, data string) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	var event string
	var data []string
	flush := func() error {
		if event == "" && len(data) == 0 {
			return nil
		}
		err := fn(event, strings.Join(data, "\n"))
		event, data = "", nil
		return err
	}

	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		switch {
		case line == "":
			if err := flush(); err != nil {
				return err
			}
		case strings.HasPrefix(line, ":"):
			// comment / keep-alive
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return flush()
}
