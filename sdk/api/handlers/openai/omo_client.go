package openai

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// omoClientIdentifiers are the client surfaces oh-my-pi presents when it calls
// this proxy. The agent identifies itself through User-Agent or Originator.
var omoClientIdentifiers = []string{"oh-my-pi", "ohmypi", "omo"}

// isOmoClientRequest reports whether a request comes from the oh-my-pi agent.
//
// omo consumes the Responses API strictly: it aborts a turn that ends without a
// terminal event and rejects a function_call item whose call_id does not follow
// the call_ convention, so requests from it must not take shortcuts that other
// clients tolerate.
func isOmoClientRequest(c *gin.Context) bool {
	if c == nil || c.Request == nil {
		return false
	}
	for _, header := range []string{"User-Agent", "Originator", "X-Client-Name"} {
		value := strings.ToLower(strings.TrimSpace(c.GetHeader(header)))
		if value == "" {
			continue
		}
		for _, id := range omoClientIdentifiers {
			if value == id || strings.HasPrefix(value, id+"/") || strings.Contains(value, id) {
				return true
			}
		}
	}
	return false
}
