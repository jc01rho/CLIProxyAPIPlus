package helps

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"math/rand/v2"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Request-body gzip as Claude Code 2.1.302 sends it (cortexkit/anthropic-auth
// b9a8bd928 request-gzip.ts). Claude Code gzips a request body of at least
// 4096 characters when it talks straight to api.anthropic.com. Before
// compressing it resets cch in the billing block to 00000 and appends a
// newline plus 0 to 256 random spaces and tabs. If the compressed body is
// rejected it resends the uncompressed, signed body once and stops
// compressing for the rest of the process.

const (
	// ClaudeRequestGzipMinChars is the smallest body, in UTF-16 code units,
	// Claude Code gzips.
	ClaudeRequestGzipMinChars = 4096
	claudeRequestGzipHost     = "api.anthropic.com"
	claudeRequestGzipLevel    = 6
	claudeRequestGzipMaxPad   = 256
)

// Skip reasons reported by DecideClaudeRequestGzip.
const (
	ClaudeRequestGzipSkipDisabled   = "disabled"
	ClaudeRequestGzipSkipHost       = "host"
	ClaudeRequestGzipSkipSize       = "below_min_size"
	ClaudeRequestGzipSkipProxy      = "proxy"
	ClaudeRequestGzipSkipLatched    = "latched_off"
	ClaudeGzipRejectionNonAnthropic = "non_anthropic_rejection"
	ClaudeGzipRejectionInvalidJSON  = "invalid_json_at_origin"
)

var claudeRequestGzipLatched atomic.Bool

// LatchClaudeRequestGzipOff stops gzip for the rest of the process.
func LatchClaudeRequestGzipOff() { claudeRequestGzipLatched.Store(true) }

// ClaudeRequestGzipLatched reports whether a rejection turned gzip off.
func ClaudeRequestGzipLatched() bool { return claudeRequestGzipLatched.Load() }

// ResetClaudeRequestGzipLatchForTest clears the process latch.
func ResetClaudeRequestGzipLatchForTest() { claudeRequestGzipLatched.Store(false) }

// ClaudeRequestGzipEnabled reports the upstream.claude.request-gzip setting.
func ClaudeRequestGzipEnabled(cfg *config.Config) bool {
	return cfg != nil && cfg.Claude.RequestGzip
}

// ClaudeRequestUsesProxy reports whether the request leaves through a
// configured or environment proxy. Claude Code sends uncompressed bodies
// through any proxy, so these sends stay uncompressed.
func ClaudeRequestUsesProxy(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, req *http.Request) bool {
	if effectiveProxyURL(ctx, cfg, auth) != "" {
		return true
	}
	if req != nil {
		if proxyURL, errProxy := http.ProxyFromEnvironment(req); errProxy != nil || proxyURL != nil {
			return true
		}
	}
	return false
}

// DecideClaudeRequestGzip decides, in Claude Code's order, whether body goes
// out gzipped. It returns the skip reason when it does not.
func DecideClaudeRequestGzip(enabled bool, target *url.URL, body []byte, proxied bool) (bool, string) {
	if !enabled {
		return false, ClaudeRequestGzipSkipDisabled
	}
	if target == nil || !strings.EqualFold(target.Scheme, "https") || !strings.EqualFold(target.Host, claudeRequestGzipHost) {
		return false, ClaudeRequestGzipSkipHost
	}
	if utf16Length(body) < ClaudeRequestGzipMinChars {
		return false, ClaudeRequestGzipSkipSize
	}
	if proxied {
		return false, ClaudeRequestGzipSkipProxy
	}
	if ClaudeRequestGzipLatched() {
		return false, ClaudeRequestGzipSkipLatched
	}
	return true, ""
}

func utf16Length(body []byte) int {
	n := 0
	for len(body) > 0 {
		r, size := utf8.DecodeRune(body)
		body = body[size:]
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// ClaudeRequestGzipPadding returns a newline and 0 to 256 random spaces and
// tabs, as Claude Code appends before compressing.
func ClaudeRequestGzipPadding(random func() float64) []byte {
	if random == nil {
		random = rand.Float64
	}
	length := int(random() * float64(claudeRequestGzipMaxPad+1))
	if length > claudeRequestGzipMaxPad {
		length = claudeRequestGzipMaxPad
	}
	padding := make([]byte, 0, length+1)
	padding = append(padding, '\n')
	for i := 0; i < length; i++ {
		if random() < 0.5 {
			padding = append(padding, ' ')
		} else {
			padding = append(padding, '\t')
		}
	}
	return padding
}

// EncodeClaudeRequestGzip returns the gzipped wire bytes for an identity body
// whose cch the caller already reset to 00000. Never sign the result.
func EncodeClaudeRequestGzip(unsignedBody []byte, random func() float64) ([]byte, error) {
	var buf bytes.Buffer
	writer, errWriter := gzip.NewWriterLevel(&buf, claudeRequestGzipLevel)
	if errWriter != nil {
		return nil, errWriter
	}
	if _, errWrite := writer.Write(unsignedBody); errWrite != nil {
		return nil, errWrite
	}
	if _, errWrite := writer.Write(ClaudeRequestGzipPadding(random)); errWrite != nil {
		return nil, errWrite
	}
	if errClose := writer.Close(); errClose != nil {
		return nil, errClose
	}
	out := buf.Bytes()
	// Claude Code's runtime writes OS byte 255 (unknown); the byte is outside the CRC.
	if len(out) > 9 {
		out[9] = 0xff
	}
	return out, nil
}

var (
	claudeGzipInvalidJSONMessages = []string{
		"The request body is not valid JSON",
		"Failed to parse request: could not parse request body as JSON",
	}
	claudeGzipBadJSONText = regexp.MustCompile(`^bad json: (?:invalid character |unexpected EOF$|unexpected end of JSON input$|EOF$)`)
)

// ClassifyClaudeGzipRejection is Claude Code's test for a rejected gzipped
// body: a 400, 403 or 415 that did not come from Anthropic (no req_ request id
// and no cf-ray), or a 400 that says the JSON could not be parsed. decodedBody
// is the start of the decoded response body. It returns "" for any other
// response, including an ordinary API 400.
func ClassifyClaudeGzipRejection(status int, header http.Header, decodedBody []byte) string {
	if status != http.StatusBadRequest && status != http.StatusForbidden && status != http.StatusUnsupportedMediaType {
		return ""
	}
	if !strings.HasPrefix(header.Get("request-id"), "req_") && header.Get("cf-ray") == "" {
		return ClaudeGzipRejectionNonAnthropic
	}
	if status == http.StatusBadRequest && claudeGzipInvalidJSONBody(decodedBody) {
		return ClaudeGzipRejectionInvalidJSON
	}
	return ""
}

func claudeGzipInvalidJSONBody(body []byte) bool {
	var parsed map[string]any
	if errUnmarshal := json.Unmarshal(body, &parsed); errUnmarshal != nil {
		return claudeGzipBadJSONText.MatchString(strings.TrimRight(string(body), " \t\r\n"))
	}
	if parsed["type"] == "error" {
		if errObj, ok := parsed["error"].(map[string]any); ok {
			message, _ := errObj["message"].(string)
			if _, okType := errObj["type"].(string); okType {
				for _, prefix := range claudeGzipInvalidJSONMessages {
					if strings.HasPrefix(message, prefix) {
						return true
					}
				}
			}
		}
		return false
	}
	errText, _ := parsed["error"].(string)
	return parsed["reason"] == "bad_json" && claudeGzipBadJSONText.MatchString(errText)
}
