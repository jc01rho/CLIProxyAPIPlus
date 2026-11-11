package openai

import (
	"context"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/tidwall/gjson"
)

// systemOneAlt marks SystemOne passthrough execution through the shared
// auth-manager pipeline. The executor switches the upstream endpoint to
// /v1/systemone and forwards the native TypeSafe payload verbatim.
const systemOneAlt = "systemone"

// systemOneQuestionTypes lists the tagged question primitives every System One
// upstream discriminates on. A question object without one of these tags is
// rejected before routing: upstreams answer such a payload with a shape error
// (400 on Alibaba, 502 from the Clef worker), and the route-fallback chain then
// spends every System One provider on the same malformed body until the client
// times out.
var systemOneQuestionTypes = map[string]struct{}{
	"noul":   {},
	"choice": {},
	"score":  {},
}

// SystemOne handles the POST /v1/systemone endpoint.
// It proxies TypeSafe SystemOne decision requests (noul/choice/score) to the
// upstream provider selected by the request model, forwarding the native
// payload and returning the raw upstream JSON (typesafe-judge compatible).
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
func (h *OpenAIAPIHandler) SystemOne(c *gin.Context) {
	rawJSON, err := handlers.ReadRequestBody(c)
	if err != nil {
		c.JSON(handlers.RequestBodyErrorStatus(err), handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}
	if !gjson.ValidBytes(rawJSON) {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Invalid request: malformed JSON body",
				Type:    "invalid_request_error",
			},
		})
		return
	}
	modelName := gjson.GetBytes(rawJSON, "model").String()
	if modelName == "" {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Invalid request: missing required field 'model'",
				Type:    "invalid_request_error",
			},
		})
		return
	}
	if !gjson.GetBytes(rawJSON, "questions").IsObject() {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Invalid request: missing required object field 'questions'",
				Type:    "invalid_request_error",
			},
		})
		return
	}
	if !gjson.GetBytes(rawJSON, "state").Exists() {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Invalid request: missing required field 'state'",
				Type:    "invalid_request_error",
			},
		})
		return
	}
	if len(gjson.GetBytes(rawJSON, "questions").Map()) == 0 {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Invalid request: 'questions' must contain at least one question",
				Type:    "invalid_request_error",
			},
		})
		return
	}
	if invalidQuestion := invalidSystemOneQuestion(rawJSON); invalidQuestion != "" {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: question %q needs a 'type' tag of noul, choice or score", invalidQuestion),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	h.handleSystemOneNonStreamingResponse(c, rawJSON)
}

// invalidSystemOneQuestion returns the id of the first question that cannot be
// tagged for upstream dispatch, or an empty string when every question carries a
// known System One type.
func invalidSystemOneQuestion(rawJSON []byte) string {
	questions := gjson.GetBytes(rawJSON, "questions")
	if !questions.IsObject() {
		return "questions"
	}
	invalid := ""
	questions.ForEach(func(key, value gjson.Result) bool {
		if !value.IsObject() {
			invalid = key.String()
			return false
		}
		if _, ok := systemOneQuestionTypes[value.Get("type").String()]; !ok {
			invalid = key.String()
			return false
		}
		return true
	})
	return invalid
}

// handleSystemOneNonStreamingResponse executes a SystemOne request via the
// auth manager and writes the raw upstream JSON back to the client.
func (h *OpenAIAPIHandler) handleSystemOneNonStreamingResponse(c *gin.Context, rawJSON []byte) {
	c.Header("Content-Type", "application/json")

	modelName := gjson.GetBytes(rawJSON, "model").String()
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	stopKeepAlive := h.StartNonStreamingKeepAlive(c, cliCtx)
	resp, upstreamHeaders, errMsg := h.ExecuteWithAuthManager(cliCtx, h.HandlerType(), modelName, rawJSON, systemOneAlt)
	stopKeepAlive()
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		cliCancel(errMsg.Error)
		return
	}
	handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
	_, _ = c.Writer.Write(resp)
	cliCancel()
}
