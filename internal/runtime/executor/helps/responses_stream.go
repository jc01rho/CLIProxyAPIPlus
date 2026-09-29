package helps

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strings"
)

// ResponsesEventReader reads complete SSE frames, not individual data lines.
// Event names are retained for servers that omit the JSON type discriminator.
type ResponsesEventReader struct {
	scanner *bufio.Scanner
}

func NewResponsesEventReader(body io.Reader) *ResponsesEventReader {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(nil, 50<<20)
	return &ResponsesEventReader{scanner: scanner}
}

func (r *ResponsesEventReader) Next() (event string, data []byte, err error) {
	var lines [][]byte
	for r.scanner.Scan() {
		line := r.scanner.Bytes()
		if len(line) == 0 {
			if len(lines) > 0 {
				return event, bytes.Join(lines, []byte("\n")), nil
			}
			event = ""
			continue
		}
		if line[0] == ':' {
			continue
		}
		field, value, _ := strings.Cut(string(line), ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			event = value
		case "data":
			lines = append(lines, []byte(value))
		default:
			// JSON errors are sometimes returned with an SSE content type.
			if bytes.HasPrefix(bytes.TrimSpace(line), []byte("{")) {
				return "", nil, fmt.Errorf("expected Responses SSE frame: %s", line)
			}
		}
	}
	if err = r.scanner.Err(); err != nil {
		return "", nil, err
	}
	// SSE dispatch requires a blank line. A partial frame at EOF is not terminal.
	return "", nil, io.EOF
}
