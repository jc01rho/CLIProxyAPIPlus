package executor

import (
	"bytes"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

const (
	// deepAIDefaultBaseURL is the api.deepai.org origin the browser chat client talks to.
	deepAIDefaultBaseURL = "https://api.deepai.org"
	// deepAIChatPath is the (deliberately odd) upstream chat endpoint.
	deepAIChatPath = "/hacking_is_a_serious_crime"
	// deepAITaskStatusPath polls background reasoning tasks.
	deepAITaskStatusPath = "/check_chat_task_status"
	// deepAIOrigin is the site origin the browser client claims.
	deepAIOrigin = "https://deepai.org"
	// deepAIUserAgent is the UA the anonymous tryit key hash is validated against.
	deepAIUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36"
	// deepAITryItSalt is the constant the live deepai.org client mixes into tryit keys.
	deepAITryItSalt = "hackers_become_a_little_stinkier_every_time_they_hack"
	// deepAITaskPollInterval and deepAITaskPollTimeout bound background-task polling.
	deepAITaskPollInterval = 1500 * time.Millisecond
	deepAITaskPollTimeout  = 180 * time.Second
	// deepAIMaxErrorBody caps how much of an upstream error body is surfaced.
	deepAIMaxErrorBody = 1 << 20
)

// deepAIResult is the parsed shape of one upstream answer.
type deepAIResult struct {
	text     string
	thinking string
	images   []string
}

// applyDeepAIHeaders sets the browser-identical headers the chat surface expects.
func applyDeepAIHeaders(req *http.Request, apiKey string) {
	if req == nil {
		return
	}
	if strings.TrimSpace(apiKey) == "" {
		apiKey = mintDeepAITryItKey(deepAIUserAgent)
	}
	req.Header.Set("api-key", apiKey)
	req.Header.Set("Origin", deepAIOrigin)
	req.Header.Set("Referer", deepAIOrigin+"/")
	req.Header.Set("User-Agent", deepAIUserAgent)
	req.Header.Set("Accept", "*/*")
}

// islandHash mirrors the MD5 look-alike the deepai.org client uses: the MD5 hex
// digest with its characters reversed. Verified byte-for-byte against the live
// client for several inputs.
func islandHash(input string) string {
	sum := md5.Sum([]byte(input))
	reversed := []byte(hex.EncodeToString(sum[:]))
	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}
	return string(reversed)
}

// mintDeepAITryItKey produces a single-use anonymous key bound to userAgent.
func mintDeepAITryItKey(userAgent string) string {
	if strings.TrimSpace(userAgent) == "" {
		userAgent = deepAIUserAgent
	}
	digits := fmt.Sprintf("%d", randUint64()%100000000000)
	hash := islandHash(userAgent + islandHash(userAgent+islandHash(userAgent+digits+deepAITryItSalt)))
	return "tryit-" + digits + "-" + hash
}

func randUint64() uint64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return uint64(time.Now().UnixNano())
	}
	var v uint64
	for i := 0; i < 8; i++ {
		v = v<<8 | uint64(b[i])
	}
	return v
}

// deepAIChatEntry is one chatHistory element.
type deepAIChatEntry struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// deepAIHistory converts an OpenAI-shaped messages array into the upstream form.
func deepAIHistory(payload []byte) []deepAIChatEntry {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return nil
	}
	out := make([]deepAIChatEntry, 0, len(messages.Array()))
	for _, msg := range messages.Array() {
		content := deepAIMessageContent(msg.Get("content"))
		if strings.TrimSpace(content) == "" {
			continue
		}
		role := "user"
		switch strings.TrimSpace(msg.Get("role").String()) {
		case "assistant":
			role = "assistant"
		case "system", "developer":
			role = "system"
		}
		out = append(out, deepAIChatEntry{Role: role, Content: content})
	}
	return out
}

// deepAIMessageContent flattens string / array-shaped message content.
func deepAIMessageContent(node gjson.Result) string {
	if !node.Exists() {
		return ""
	}
	if node.Type == gjson.String {
		return node.String()
	}
	if node.IsArray() {
		parts := make([]string, 0, len(node.Array()))
		for _, part := range node.Array() {
			switch strings.TrimSpace(part.Get("type").String()) {
			case "text", "input_text", "output_text":
				if text := part.Get("text").String(); text != "" {
					parts = append(parts, text)
				}
			case "image_url":
				parts = append(parts, "[image attached - not supported by this provider]")
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	}
	if node.IsObject() {
		return node.Get("text").String()
	}
	return node.String()
}

// deepAISessionUUID derives a stable conversation identity from the first user
// turn so retries of the same conversation keep the same upstream session.
func deepAISessionUUID(history []deepAIChatEntry) string {
	anchor := ""
	for _, entry := range history {
		if entry.Role == "user" {
			anchor = entry.Content
			break
		}
	}
	if anchor == "" && len(history) > 0 {
		anchor = history[0].Content
	}
	sum := sha256.Sum256([]byte(anchor))
	sum[6] = (sum[6] & 0x0f) | 0x40
	sum[8] = (sum[8] & 0x3f) | 0x80
	h := hex.EncodeToString(sum[:16])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// deepAIBuildForm assembles the multipart body the browser posts.
func deepAIBuildForm(payload []byte, upstreamModel string, webAccess bool) (body []byte, contentType string, history []deepAIChatEntry, err error) {
	history = deepAIHistory(payload)
	if len(history) == 0 {
		return nil, "", nil, statusErr{code: http.StatusBadRequest, msg: "deepai: messages must contain non-empty content"}
	}
	historyJSON, errMarshal := json.Marshal(history)
	if errMarshal != nil {
		return nil, "", nil, errMarshal
	}

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	fields := [][2]string{
		{"chat_style", "chat"},
		{"chatHistory", string(historyJSON)},
		{"model", upstreamModel},
		{"hacker_is_stinky", "very_stinky"},
		{"session_uuid", deepAISessionUUID(history)},
		{"tool_activity_support", "1"},
		{"thinking_image_tool_support", "1"},
		{"enabled_tools", `["image_generator","image_editor"]`},
	}
	if webAccess {
		fields = append(fields, [2]string{"web_access_enabled", "true"})
	}
	for _, field := range fields {
		if errField := writer.WriteField(field[0], field[1]); errField != nil {
			return nil, "", nil, errField
		}
	}
	if errClose := writer.Close(); errClose != nil {
		return nil, "", nil, errClose
	}
	return buf.Bytes(), writer.FormDataContentType(), history, nil
}

// deepAIWantsWebAccess reports whether the client asked for web access.
func deepAIWantsWebAccess(payloads ...[]byte) bool {
	for _, payload := range payloads {
		if len(payload) == 0 {
			continue
		}
		if gjson.GetBytes(payload, "web_access").Bool() || gjson.GetBytes(payload, "webSearch").Bool() {
			return true
		}
	}
	return false
}

// deepAITaskID returns the background task id when the endpoint answered with one.
func deepAITaskID(body []byte, contentType string) string {
	if !strings.Contains(strings.ToLower(contentType), "json") {
		return ""
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return ""
	}
	return strings.TrimSpace(gjson.GetBytes(trimmed, "task_id").String())
}

// deepAIJSONStatusRefusal detects the short JSON status bodies delivered with HTTP 200.
func deepAIJSONStatusRefusal(body []byte) string {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || len(trimmed) > 512 || trimmed[0] != '{' {
		return ""
	}
	status := strings.TrimSpace(gjson.GetBytes(trimmed, "status").String())
	if status == "" {
		return ""
	}
	return "deepai: upstream refused the request (" + status + ")"
}

// parseDeepAIStream splits an accumulated upstream body into prose, thinking and
// the trailing JSON payload. It is safe to call on partial data: the returned
// prose is always a prefix-stable view of the final answer.
func parseDeepAIStream(raw string) deepAIResult {
	text := stripDeepAIActivityPackets(raw)

	var payload gjson.Result
	var payloadRaw []byte
	if idx := strings.Index(text, "\x1c"); idx != -1 {
		candidate := strings.TrimSpace(text[idx+1:])
		if candidate != "" && gjson.Valid(candidate) {
			payload = gjson.Parse(candidate)
			payloadRaw = []byte(candidate)
		}
		text = text[:idx]
	}

	thinking := ""
	const (
		thinkStart = "\x1dTHINKING_START"
		thinkEnd   = "\x1dTHINKING_END"
	)
	if start := strings.Index(text, thinkStart); start != -1 {
		after := ""
		if end := strings.Index(text, thinkEnd); end != -1 && end > start {
			body := text[start+len(thinkStart) : end]
			thinking = deepAIThinkingBody(body)
			after = text[end+len(thinkEnd):]
		} else {
			body := text[start+len(thinkStart):]
			if sep := strings.Index(body, "\x1e"); sep != -1 {
				thinking = withholdDeepAIPartialMarker(body[sep+1:], thinkEnd)
			}
		}
		text = text[:start] + after
	}
	text = withholdDeepAIPartialMarker(text, thinkStart)

	text = stripDeepAIControls(text)
	thinking = stripDeepAIControls(thinking)

	return deepAIResult{text: text, thinking: thinking, images: deepAIImages(payload, payloadRaw)}
}

// deepAIThinkingBody drops the leading duration marker ("12s\x1E") of a thinking block.
func deepAIThinkingBody(body string) string {
	if sep := strings.Index(body, "\x1e"); sep != -1 {
		return body[sep+1:]
	}
	return body
}

// stripDeepAIActivityPackets removes paired \x1C{json}\x1C activity packets.
func stripDeepAIActivityPackets(raw string) string {
	text := raw
	for {
		start := strings.Index(text, "\x1c")
		if start == -1 {
			return text
		}
		end := strings.Index(text[start+1:], "\x1c")
		if end == -1 {
			return text
		}
		end += start + 1
		interior := text[start+1 : end]
		if !gjson.Valid(interior) || !gjson.Get(interior, "tool_activity").Exists() {
			// Not an activity packet: leave it for the trailing-payload split.
			return text
		}
		text = text[:start] + text[end+1:]
	}
}

// withholdDeepAIPartialMarker keeps an ambiguous suffix buffered so half a
// marker never leaks into the visible prose.
func withholdDeepAIPartialMarker(value, marker string) string {
	max := len(value)
	if len(marker)-1 < max {
		max = len(marker) - 1
	}
	for length := max; length > 0; length-- {
		if strings.HasPrefix(marker, value[len(value)-length:]) {
			return value[:len(value)-length]
		}
	}
	return value
}

// stripDeepAIControls removes stray packet separators from visible text.
func stripDeepAIControls(value string) string {
	if !strings.ContainsAny(value, "\x1c\x1d\x1e") {
		return value
	}
	return strings.Map(func(r rune) rune {
		switch r {
		case '\x1c', '\x1d', '\x1e':
			return -1
		}
		return r
	}, value)
}

// deepAIImages collects image URLs carried by the trailing payload.
func deepAIImages(payload gjson.Result, raw []byte) []string {
	if len(raw) == 0 || !payload.IsObject() {
		return nil
	}
	urls := make([]string, 0, 2)
	pick := func(node gjson.Result) {
		for _, key := range []string{"share_url", "url", "output_url"} {
			if value := strings.TrimSpace(node.Get(key).String()); value != "" {
				urls = append(urls, value)
				return
			}
		}
	}
	if node := payload.Get("images"); node.IsArray() {
		for _, image := range node.Array() {
			if image.Type == gjson.String {
				if value := strings.TrimSpace(image.String()); value != "" {
					urls = append(urls, value)
				}
				continue
			}
			pick(image)
		}
	}
	if len(urls) == 0 {
		pick(payload)
	}
	return urls
}

// deepAIModelDefault maps a client model name to the upstream model selector.
func deepAIModelDefault(model string) string {
	name := strings.ToLower(strings.TrimSpace(model))
	name = strings.TrimPrefix(name, "deepai/")
	switch name {
	case "genius", "deepai-genius":
		return "genius"
	case "super_genius", "super-genius", "deepai-super-genius", "deepai-super_genius":
		return "super_genius"
	case "", "standard", "deepai-standard":
		return "standard"
	}
	return name
}

// deepAIImageMarkdown renders generated images as markdown, matching the
// deepai2api gateway's shape.
func deepAIImageMarkdown(images []string) string {
	if len(images) == 0 {
		return ""
	}
	lines := make([]string, 0, len(images))
	for _, url := range images {
		lines = append(lines, "![generated image]("+url+")")
	}
	return "\n\n" + strings.Join(lines, "\n")
}
