package antigravity

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultHTTPSPort = 443
	defaultProxyPort = 8080

	// DefaultAgyResponseHeaderTimeout is the default timeout for connect and response headers.
	DefaultAgyResponseHeaderTimeout = 180 * time.Second
)

// AgyTransportOptions configures the raw agy CLI HTTP/1.1 transport.
type AgyTransportOptions struct {
	// Timeout is the maximum duration to connect and receive HTTP response headers.
	Timeout time.Duration
	// OnDebug receives debug messages emitted during transport operation.
	OnDebug func(message string)
}

// AgyRequestInit is the Go equivalent of the RequestInit subset supported by the agy transport.
type AgyRequestInit struct {
	Method  string
	Headers map[string]string
	Body    []byte
}

// HeaderPair is an ordered HTTP header key/value pair.
type HeaderPair struct {
	Key   string
	Value string
}

type parsedResponseHead struct {
	status     int
	statusText string
	headers    http.Header
	chunked    bool
	gzip       bool
}

func headersToRecord(headers map[string]string) map[string]string {
	result := make(map[string]string)
	for key, value := range headers {
		result[strings.ToLower(key)] = value
	}
	return result
}

func getHeader(headers map[string]string, name string) string {
	return headers[strings.ToLower(name)]
}

func shouldUseChunkedBody(parsedURL *url.URL) bool {
	return strings.Contains(parsedURL.Path, ":streamGenerateContent")
}

// BuildAgyCLIHeaderPairs builds the exact header order emitted by the agy CLI transport.
func BuildAgyCLIHeaderPairs(rawURL string, init AgyRequestInit) ([]HeaderPair, error) {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	headers := headersToRecord(init.Headers)
	host := parsedURL.Hostname()
	if parsedURL.Port() != "" {
		host = parsedURL.Hostname() + ":" + parsedURL.Port()
	}
	userAgent := getHeader(headers, "User-Agent")
	if userAgent == "" {
		userAgent = "antigravity/cli/1.0.4 darwin/arm64"
	}
	authorization := getHeader(headers, "Authorization")
	contentType := getHeader(headers, "Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	acceptEncoding := getHeader(headers, "Accept-Encoding")
	if acceptEncoding == "" {
		acceptEncoding = "gzip"
	}
	chunked := shouldUseChunkedBody(parsedURL)

	pairs := []HeaderPair{{Key: "Host", Value: host}, {Key: "User-Agent", Value: userAgent}}
	if chunked {
		pairs = append(pairs, HeaderPair{Key: "Transfer-Encoding", Value: "chunked"})
	} else {
		pairs = append(pairs, HeaderPair{Key: "Content-Length", Value: strconv.Itoa(len(init.Body))})
	}
	if authorization != "" {
		pairs = append(pairs, HeaderPair{Key: "Authorization", Value: authorization})
	}
	pairs = append(pairs, HeaderPair{Key: "Content-Type", Value: contentType})
	pairs = append(pairs, HeaderPair{Key: "Accept-Encoding", Value: acceptEncoding})
	return pairs, nil
}

func noProxyIncludes(hostname string) bool {
	raw := os.Getenv("NO_PROXY")
	if raw == "" {
		raw = os.Getenv("no_proxy")
	}
	if raw == "" {
		return false
	}
	host := strings.ToLower(hostname)
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(strings.ToLower(entry))
		if entry == "" {
			continue
		}
		if entry == "*" {
			return true
		}
		if strings.HasPrefix(entry, ".") {
			if strings.HasSuffix(host, entry) {
				return true
			}
			continue
		}
		if host == entry || strings.HasSuffix(host, "."+entry) {
			return true
		}
	}
	return false
}

func getHTTPSProxy(targetURL *url.URL) *url.URL {
	if targetURL.Scheme != "https" || noProxyIncludes(targetURL.Hostname()) {
		return nil
	}
	rawProxy := os.Getenv("HTTPS_PROXY")
	if rawProxy == "" {
		rawProxy = os.Getenv("https_proxy")
	}
	if rawProxy == "" {
		rawProxy = os.Getenv("ALL_PROXY")
	}
	if rawProxy == "" {
		rawProxy = os.Getenv("all_proxy")
	}
	if rawProxy == "" {
		return nil
	}
	proxyURL, err := url.Parse(rawProxy)
	if err != nil {
		return nil
	}
	return proxyURL
}

func waitForHead(conn net.Conn, timeout time.Duration, onTimeout func()) (string, []byte, error) {
	if timeout > 0 {
		_ = conn.SetReadDeadline(time.Now().Add(timeout))
		defer conn.SetReadDeadline(time.Time{})
	}
	buffer := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		marker := bytes.Index(buffer, []byte("\r\n\r\n"))
		if marker >= 0 {
			head := string(buffer[:marker])
			leftover := append([]byte(nil), buffer[marker+4:]...)
			return head, leftover, nil
		}
		n, err := conn.Read(tmp)
		if n > 0 {
			buffer = append(buffer, tmp[:n]...)
			continue
		}
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				if onTimeout != nil {
					onTimeout()
				}
				return "", nil, fmt.Errorf("Antigravity request timed out waiting for response headers after %dms", timeout.Milliseconds())
			}
			return "", nil, err
		}
	}
}

type preReadConn struct {
	net.Conn
	mu       sync.Mutex
	leftover []byte
}

func (c *preReadConn) Read(p []byte) (int, error) {
	c.mu.Lock()
	if len(c.leftover) > 0 {
		n := copy(p, c.leftover)
		c.leftover = c.leftover[n:]
		c.mu.Unlock()
		return n, nil
	}
	c.mu.Unlock()
	return c.Conn.Read(p)
}

func connectViaProxy(ctx context.Context, proxyURL *url.URL, targetURL *url.URL, timeout time.Duration, onDebug func(string)) (*tls.Conn, error) {
	if timeout <= 0 {
		timeout = DefaultAgyResponseHeaderTimeout
	}
	dialer := &net.Dialer{Timeout: timeout}
	proxyPort := proxyURL.Port()
	if proxyPort == "" {
		proxyPort = strconv.Itoa(defaultProxyPort)
	}
	proxySocket, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(proxyURL.Hostname(), proxyPort))
	if err != nil {
		if onDebug != nil {
			onDebug(fmt.Sprintf("agy transport proxy connect timeout after %dms", timeout.Milliseconds()))
		}
		return nil, fmt.Errorf("Antigravity request timed out connecting to HTTPS proxy after %dms", timeout.Milliseconds())
	}

	targetHost := targetURL.Hostname()
	targetPort := targetURL.Port()
	if targetPort == "" {
		targetPort = strconv.Itoa(defaultHTTPSPort)
	}
	auth := ""
	if proxyURL.User != nil {
		username := proxyURL.User.Username()
		password, _ := proxyURL.User.Password()
		auth = "Proxy-Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password)) + "\r\n"
	}
	connectRequest := fmt.Sprintf("CONNECT %s:%s HTTP/1.1\r\nHost: %s:%s\r\n%s\r\n", targetHost, targetPort, targetHost, targetPort, auth)
	if _, errWrite := io.WriteString(proxySocket, connectRequest); errWrite != nil {
		_ = proxySocket.Close()
		return nil, errWrite
	}
	head, leftover, errHead := waitForHead(proxySocket, timeout, func() {
		if onDebug != nil {
			onDebug(fmt.Sprintf("agy transport proxy CONNECT response timeout after %dms", timeout.Milliseconds()))
		}
		_ = proxySocket.Close()
	})
	if errHead != nil {
		_ = proxySocket.Close()
		return nil, errHead
	}
	statusLine := head
	if idx := strings.Index(statusLine, "\r\n"); idx >= 0 {
		statusLine = statusLine[:idx]
	}
	if !isProxyConnectSuccess(statusLine) {
		_ = proxySocket.Close()
		if statusLine == "" {
			statusLine = "unknown"
		}
		return nil, fmt.Errorf("Proxy CONNECT failed: %s", statusLine)
	}

	wrapped := &preReadConn{Conn: proxySocket, leftover: leftover}
	tlsSocket := tls.Client(wrapped, &tls.Config{ServerName: targetHost})
	if timeout > 0 {
		_ = tlsSocket.SetDeadline(time.Now().Add(timeout))
	}
	if errHandshake := tlsSocket.HandshakeContext(ctx); errHandshake != nil {
		if onDebug != nil {
			onDebug(fmt.Sprintf("agy transport proxy TLS handshake timeout after %dms", timeout.Milliseconds()))
		}
		_ = tlsSocket.Close()
		return nil, fmt.Errorf("Antigravity request timed out during proxy TLS handshake after %dms", timeout.Milliseconds())
	}
	_ = tlsSocket.SetDeadline(time.Time{})
	return tlsSocket, nil
}

func isProxyConnectSuccess(statusLine string) bool {
	fields := strings.Fields(statusLine)
	if len(fields) < 2 || (fields[0] != "HTTP/1.0" && fields[0] != "HTTP/1.1") {
		return false
	}
	return len(fields[1]) == 3 && fields[1][0] == '2'
}

func connectDirect(ctx context.Context, targetURL *url.URL, timeout time.Duration, onDebug func(string)) (*tls.Conn, error) {
	if timeout <= 0 {
		timeout = DefaultAgyResponseHeaderTimeout
	}
	port := targetURL.Port()
	if port == "" {
		port = strconv.Itoa(defaultHTTPSPort)
	}
	dialer := &net.Dialer{Timeout: timeout}
	tcpConn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(targetURL.Hostname(), port))
	if err != nil {
		if onDebug != nil {
			onDebug(fmt.Sprintf("agy transport TLS connect timeout after %dms", timeout.Milliseconds()))
		}
		return nil, fmt.Errorf("Antigravity request timed out connecting after %dms", timeout.Milliseconds())
	}
	tlsSocket := tls.Client(tcpConn, &tls.Config{ServerName: targetURL.Hostname()})
	if timeout > 0 {
		_ = tlsSocket.SetDeadline(time.Now().Add(timeout))
	}
	if errHandshake := tlsSocket.HandshakeContext(ctx); errHandshake != nil {
		if onDebug != nil {
			onDebug(fmt.Sprintf("agy transport TLS connect timeout after %dms", timeout.Milliseconds()))
		}
		_ = tlsSocket.Close()
		return nil, fmt.Errorf("Antigravity request timed out connecting after %dms", timeout.Milliseconds())
	}
	_ = tlsSocket.SetDeadline(time.Time{})
	return tlsSocket, nil
}

func connectTLS(ctx context.Context, targetURL *url.URL, timeout time.Duration, onDebug func(string)) (*tls.Conn, error) {
	if proxyURL := getHTTPSProxy(targetURL); proxyURL != nil {
		return connectViaProxy(ctx, proxyURL, targetURL, timeout, onDebug)
	}
	return connectDirect(ctx, targetURL, timeout, onDebug)
}

func serializeRequest(parsedURL *url.URL, init AgyRequestInit) ([]byte, error) {
	method := init.Method
	if method == "" {
		method = http.MethodPost
	}
	path := parsedURL.EscapedPath()
	if path == "" {
		path = "/"
	}
	if parsedURL.RawQuery != "" {
		path += "?" + parsedURL.RawQuery
	}
	pairs, err := BuildAgyCLIHeaderPairs(parsedURL.String(), init)
	if err != nil {
		return nil, err
	}
	var builder strings.Builder
	builder.WriteString(method)
	builder.WriteByte(' ')
	builder.WriteString(path)
	builder.WriteString(" HTTP/1.1\r\n")
	for _, pair := range pairs {
		builder.WriteString(pair.Key)
		builder.WriteString(": ")
		builder.WriteString(pair.Value)
		builder.WriteString("\r\n")
	}
	builder.WriteString("\r\n")
	head := []byte(builder.String())
	if len(init.Body) == 0 {
		return head, nil
	}
	if !shouldUseChunkedBody(parsedURL) {
		return append(head, init.Body...), nil
	}
	request := make([]byte, 0, len(head)+len(init.Body)+32)
	request = append(request, head...)
	request = append(request, []byte(strconv.FormatInt(int64(len(init.Body)), 16)+"\r\n")...)
	request = append(request, init.Body...)
	request = append(request, []byte("\r\n0\r\n\r\n")...)
	return request, nil
}

func parseResponseHead(head string) (parsedResponseHead, error) {
	lines := strings.Split(head, "\r\n")
	statusLine := ""
	if len(lines) > 0 {
		statusLine = lines[0]
		lines = lines[1:]
	}
	fields := strings.Fields(statusLine)
	if len(fields) < 2 || (fields[0] != "HTTP/1.0" && fields[0] != "HTTP/1.1") || len(fields[1]) != 3 {
		return parsedResponseHead{}, fmt.Errorf("Invalid HTTP response: %s", statusLine)
	}
	status, err := strconv.Atoi(fields[1])
	if err != nil {
		return parsedResponseHead{}, fmt.Errorf("Invalid HTTP response: %s", statusLine)
	}
	statusText := ""
	if len(statusLine) > len(fields[0])+1+len(fields[1]) {
		statusText = strings.TrimSpace(statusLine[len(fields[0])+1+len(fields[1]):])
	}
	headers := make(http.Header)
	chunked := false
	gzipped := false
	for _, line := range lines {
		idx := strings.Index(line, ":")
		if idx <= 0 {
			continue
		}
		key := line[:idx]
		value := strings.TrimSpace(line[idx+1:])
		lowerKey := strings.ToLower(key)
		lowerValue := strings.ToLower(value)
		if lowerKey == "transfer-encoding" && strings.Contains(lowerValue, "chunked") {
			chunked = true
			continue
		}
		if lowerKey == "content-encoding" && strings.Contains(lowerValue, "gzip") {
			gzipped = true
			continue
		}
		if gzipped && lowerKey == "content-length" {
			continue
		}
		headers.Add(key, value)
	}
	return parsedResponseHead{status: status, statusText: statusText, headers: headers, chunked: chunked, gzip: gzipped}, nil
}

type chunkedDecodeReader struct {
	r         *bufio.Reader
	remaining int64
	done      bool
	needCRLF  bool
}

func newChunkedDecodeReader(r io.Reader) *chunkedDecodeReader {
	return &chunkedDecodeReader{r: bufio.NewReader(r)}
}

func (r *chunkedDecodeReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	for r.remaining == 0 {
		if r.needCRLF {
			if err := r.readCRLF(); err != nil {
				return 0, err
			}
			r.needCRLF = false
		}
		line, err := r.r.ReadString('\n')
		if err != nil {
			return 0, err
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		sizeText := strings.TrimSpace(strings.SplitN(line, ";", 2)[0])
		size, errParse := strconv.ParseInt(sizeText, 16, 64)
		if errParse != nil {
			return 0, fmt.Errorf("Invalid chunk size: %s", line)
		}
		if size == 0 {
			r.done = true
			return 0, io.EOF
		}
		r.remaining = size
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.r.Read(p)
	r.remaining -= int64(n)
	if r.remaining == 0 {
		r.needCRLF = true
	}
	return n, err
}

func (r *chunkedDecodeReader) readCRLF() error {
	first, err := r.r.ReadByte()
	if err != nil {
		return err
	}
	second, err := r.r.ReadByte()
	if err != nil {
		return err
	}
	if first != '\r' || second != '\n' {
		return fmt.Errorf("invalid chunk terminator")
	}
	return nil
}

type responseBodyReadCloser struct {
	reader  io.Reader
	conn    net.Conn
	onClose func()
	once    sync.Once
}

func (b *responseBodyReadCloser) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	if errors.Is(err, io.EOF) {
		b.Close()
	}
	return n, err
}

func (b *responseBodyReadCloser) Close() error {
	var err error
	b.once.Do(func() {
		if b.onClose != nil {
			b.onClose()
		}
		err = b.conn.Close()
	})
	return err
}

func buildResponseBody(ctx context.Context, conn net.Conn, leftover []byte, head parsedResponseHead) (io.ReadCloser, error) {
	baseReader := io.MultiReader(bytes.NewReader(leftover), conn)
	var reader io.Reader = baseReader
	if head.chunked {
		reader = newChunkedDecodeReader(reader)
	}
	if head.gzip {
		gzipReader, err := gzip.NewReader(reader)
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
		reader = gzipReader
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	return &responseBodyReadCloser{reader: reader, conn: conn, onClose: func() { close(done) }}, nil
}

// FetchWithAgyCLITransport executes an HTTPS request using manual agy CLI HTTP/1.1 serialization.
func FetchWithAgyCLITransport(ctx context.Context, rawURL string, init AgyRequestInit, options AgyTransportOptions) (*http.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if parsedURL.Scheme != "https" {
		return nil, fmt.Errorf("agy transport only supports https URLs: %s", rawURL)
	}
	requestBytes, err := serializeRequest(parsedURL, init)
	if err != nil {
		return nil, err
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = DefaultAgyResponseHeaderTimeout
	}
	if options.OnDebug != nil {
		options.OnDebug(fmt.Sprintf("agy transport connecting to %s with header timeout %dms", parsedURL.Hostname(), timeout.Milliseconds()))
	}
	conn, err := connectTLS(ctx, parsedURL, timeout, options.OnDebug)
	if err != nil {
		return nil, err
	}
	abortDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-abortDone:
		}
	}()
	defer close(abortDone)

	if _, errWrite := conn.Write(requestBytes); errWrite != nil {
		_ = conn.Close()
		return nil, errWrite
	}
	if options.OnDebug != nil {
		options.OnDebug(fmt.Sprintf("agy transport request dispatched (%d bytes)", len(requestBytes)))
	}
	head, leftover, errHead := waitForHead(conn, timeout, func() {
		if options.OnDebug != nil {
			options.OnDebug(fmt.Sprintf("agy transport response header timeout after %dms", timeout.Milliseconds()))
		}
		_ = conn.Close()
	})
	if errHead != nil {
		_ = conn.Close()
		return nil, errHead
	}
	parsedHead, errParse := parseResponseHead(head)
	if errParse != nil {
		_ = conn.Close()
		return nil, errParse
	}
	if options.OnDebug != nil {
		options.OnDebug(fmt.Sprintf("agy transport response headers received: %d %s", parsedHead.status, parsedHead.statusText))
	}
	body, errBody := buildResponseBody(ctx, conn, leftover, parsedHead)
	if errBody != nil {
		return nil, errBody
	}
	return &http.Response{
		StatusCode: parsedHead.status,
		Status:     fmt.Sprintf("%d %s", parsedHead.status, parsedHead.statusText),
		Header:     parsedHead.headers,
		Body:       body,
		Request:    &http.Request{Method: init.Method, URL: parsedURL},
	}, nil
}
