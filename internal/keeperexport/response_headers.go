package keeperexport

import (
	"mime"
	"net/http"
	"strconv"
	"strings"
)

// validateKeeperResponseHeaders enforces the frozen response media/encoding
// contract for any Keeper response. The wire contract requires the response
// Content-Type to be application/json with the utf-8 charset. A non-JSON
// response, missing/wrong charset, any Content-Encoding, or a body larger than
// maxBytes is a protocol-layer violation.
func validateKeeperResponseHeaders(response *http.Response, maxBytes int64, requireBody bool) *Error {
	if response == nil {
		return protocolError("keeper_invalid_response")
	}
	if response.Header.Get("Content-Encoding") != "" {
		return protocolError("keeper_invalid_response")
	}
	rawType := response.Header.Get("Content-Type")
	mediaType, params, err := mime.ParseMediaType(rawType)
	if err != nil {
		return protocolError("keeper_invalid_response")
	}
	if !strings.EqualFold(mediaType, "application/json") || len(params) != 1 || !strings.EqualFold(strings.TrimSpace(params["charset"]), "utf-8") {
		return protocolError("keeper_invalid_response")
	}
	if contentLength := response.Header.Get("Content-Length"); contentLength != "" {
		if declared, err := strconv.ParseInt(contentLength, 10, 64); err == nil && declared > maxBytes {
			return protocolError("keeper_invalid_response")
		}
	}
	if requireBody && response.Body == nil {
		return protocolError("keeper_invalid_response")
	}
	return nil
}
