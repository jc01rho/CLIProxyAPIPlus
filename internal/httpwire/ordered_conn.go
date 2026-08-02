// Package httpwire contains narrowly scoped HTTP/1.1 wire helpers.
package httpwire

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
)

const maxBufferedRequestHeader = 1 << 20

// RequestHeaderOrder returns the desired header-name order for one HTTP/1.1
// request. Names are compared case-insensitively. Headers omitted from the
// returned list retain their original relative order after the listed headers.
type RequestHeaderOrder func(method, requestTarget string) []string

// NewOrderedRequestConn wraps conn and rewrites only HTTP/1.1 request-header
// order. Request lines, header casing and values, and body bytes remain intact.
func NewOrderedRequestConn(conn net.Conn, order RequestHeaderOrder) net.Conn {
	if conn == nil || order == nil {
		return conn
	}
	return &orderedRequestConn{Conn: conn, order: order}
}

type orderedRequestConn struct {
	net.Conn
	order RequestHeaderOrder

	mu            sync.Mutex
	header        []byte
	bodyRemaining int64
	passthrough   bool
}

func (c *orderedRequestConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.passthrough {
		return c.Conn.Write(p)
	}

	originalLength := len(p)
	remaining := p
	for len(remaining) > 0 {
		if c.bodyRemaining > 0 {
			bodyBytes := int64(len(remaining))
			if bodyBytes > c.bodyRemaining {
				bodyBytes = c.bodyRemaining
			}
			if errWrite := writeAll(c.Conn, remaining[:bodyBytes]); errWrite != nil {
				return 0, errWrite
			}
			remaining = remaining[bodyBytes:]
			c.bodyRemaining -= bodyBytes
			continue
		}

		c.header = append(c.header, remaining...)
		headerEnd := bytes.Index(c.header, []byte("\r\n\r\n"))
		if headerEnd < 0 {
			if len(c.header) > maxBufferedRequestHeader {
				return 0, fmt.Errorf("httpwire: request header exceeds %d bytes", maxBufferedRequestHeader)
			}
			return originalLength, nil
		}

		headerEnd += len("\r\n\r\n")
		header := c.header[:headerEnd]
		body := c.header[headerEnd:]
		c.header = nil

		ordered, contentLength, chunked := orderRequestHeader(header, c.order)
		if errWrite := writeAll(c.Conn, ordered); errWrite != nil {
			return 0, errWrite
		}
		if chunked {
			if errWrite := writeAll(c.Conn, body); errWrite != nil {
				return 0, errWrite
			}
			c.passthrough = true
			return originalLength, nil
		}
		c.bodyRemaining = contentLength
		remaining = body
	}
	return originalLength, nil
}

func orderRequestHeader(header []byte, order RequestHeaderOrder) ([]byte, int64, bool) {
	lines := bytes.Split(header[:len(header)-len("\r\n\r\n")], []byte("\r\n"))
	if len(lines) == 0 {
		return header, 0, false
	}
	requestParts := strings.SplitN(string(lines[0]), " ", 3)
	if len(requestParts) != 3 {
		return header, requestContentLength(lines[1:]), requestUsesChunkedEncoding(lines[1:])
	}

	desired := order(requestParts[0], requestParts[1])
	if len(desired) == 0 {
		return header, requestContentLength(lines[1:]), requestUsesChunkedEncoding(lines[1:])
	}

	headerLines := lines[1:]
	used := make([]bool, len(headerLines))
	orderedLines := make([][]byte, 0, len(lines))
	orderedLines = append(orderedLines, lines[0])
	for _, name := range desired {
		for index, line := range headerLines {
			if used[index] || !headerLineNamed(line, name) {
				continue
			}
			orderedLines = append(orderedLines, line)
			used[index] = true
		}
	}
	for index, line := range headerLines {
		if !used[index] {
			orderedLines = append(orderedLines, line)
		}
	}

	var output bytes.Buffer
	for _, line := range orderedLines {
		output.Write(line)
		output.WriteString("\r\n")
	}
	output.WriteString("\r\n")
	return output.Bytes(), requestContentLength(headerLines), requestUsesChunkedEncoding(headerLines)
}

func headerLineNamed(line []byte, name string) bool {
	colon := bytes.IndexByte(line, ':')
	return colon > 0 && strings.EqualFold(string(line[:colon]), name)
}

func requestContentLength(lines [][]byte) int64 {
	for _, line := range lines {
		if !headerLineNamed(line, "Content-Length") {
			continue
		}
		colon := bytes.IndexByte(line, ':')
		value := strings.TrimSpace(string(line[colon+1:]))
		length, errParse := strconv.ParseInt(value, 10, 64)
		if errParse == nil && length > 0 {
			return length
		}
		return 0
	}
	return 0
}

func requestUsesChunkedEncoding(lines [][]byte) bool {
	for _, line := range lines {
		if !headerLineNamed(line, "Transfer-Encoding") {
			continue
		}
		colon := bytes.IndexByte(line, ':')
		for _, encoding := range strings.Split(string(line[colon+1:]), ",") {
			if strings.EqualFold(strings.TrimSpace(encoding), "chunked") {
				return true
			}
		}
	}
	return false
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, errWrite := writer.Write(data)
		if errWrite != nil {
			return errWrite
		}
		if written <= 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}
