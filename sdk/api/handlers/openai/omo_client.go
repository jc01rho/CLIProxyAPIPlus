package openai

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// omoOriginator is the originator value the omo agent sends.
//
// omo (package omo-ai, github.com/code-yeongyu/oh-my-openagent) is a distinct
// project from oh-my-pi; only the Cascade wire format was borrowed from the
// latter. Its launcher injects a brand profile whose name, userAgent and
// originator are all "omo", so it sends "Originator: omo" and a user agent of
// the form "omo/<version> (<platform>; <runtime>; <arch>)".
const omoOriginator = "omo"

// omoUserAgentPrefixes are the user-agent identities the omo agent presents.
var omoUserAgentPrefixes = []string{"omo/", "omo-coding-agent"}

// isOmoClientRequest reports whether a request comes from the omo agent.
//
// omo consumes the Responses API strictly: it aborts a turn that ends without a
// terminal event and rejects a function_call item whose call_id does not follow
// the call_ convention, so requests from it must not take shortcuts that other
// clients tolerate.
func isOmoClientRequest(c *gin.Context) bool {
	if c == nil || c.Request == nil {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(c.GetHeader("Originator")), omoOriginator) {
		return true
	}
	agent := strings.ToLower(strings.TrimSpace(c.GetHeader("User-Agent")))
	if agent == "" {
		return false
	}
	for _, prefix := range omoUserAgentPrefixes {
		if strings.HasPrefix(agent, prefix) {
			return true
		}
	}
	return false
}
