package zcode

import "strings"

// officialModelIDs is the ZCode 4.0.0 spelling of the GLM model ids. The app
// sends these on the wire while the proxy catalog keeps lowercase ids, so the
// executor rewrites the request model right before it is sent (omo-zcode-oauth
// appWireNames). glm-5.3-flashx is lowercase in the app itself.
var officialModelIDs = []string{
	"GLM-5.3",
	"GLM-5.3-Flash",
	"glm-5.3-flashx",
	"GLM-5V-Turbo",
	"GLM-5.2",
	"GLM-5.1",
	"GLM-5.1-Highspeed",
	"GLM-5",
	"GLM-5-Turbo",
	"GLM-4.7",
	"GLM-4.7-FlashX",
	"GLM-4.7-Flash",
	"GLM-4.6",
	"GLM-4.5-Air",
	"GLM-4.5",
	"GLM-4.6V",
	"GLM-4.6V-Flash",
	"GLM-4.6V-FlashX",
	"GLM-4.1V-Thinking-FlashX",
	"GLM-4.1V-Thinking-Flash",
	"GLM-4-FlashX-250414",
	"GLM-4-Flash-250414",
	"GLM-4V-Flash",
}

var officialModelByLower = func() map[string]string {
	m := make(map[string]string, len(officialModelIDs))
	for _, id := range officialModelIDs {
		m[strings.ToLower(id)] = id
	}
	return m
}()

// OfficialModelID returns the ZCode wire spelling for a catalog model id.
// Ids the app already knows use its 4.0.0 spelling. For any other id,
// displayName is used only when it differs from the id in letter case alone
// (a friendly name never becomes a wire id); everything else is sent as is.
func OfficialModelID(id, displayName string) string {
	if official, ok := officialModelByLower[strings.ToLower(id)]; ok {
		return official
	}
	if displayName != "" && strings.EqualFold(displayName, id) {
		return displayName
	}
	return id
}
