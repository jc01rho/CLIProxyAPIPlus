package openai

import (
	"bytes"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
)

// responsesTerminalGuard tracks whether a terminal response event reached the
// client on the chat-to-responses conversion path.
//
// The converter only emits response.completed once it has started and has seen
// the [DONE] marker, so an upstream that produces no chunks at all, or that
// closes before the converter starts, would leave the stream unterminated.
// Clients then report that the stream ended before a terminal response event.
type responsesTerminalGuard struct {
	emitted bool
}

// write forwards converter output and records any terminal event it carries.
func (g *responsesTerminalGuard) write(c *gin.Context, outputs [][]byte) {
	for _, out := range outputs {
		if len(out) == 0 {
			continue
		}
		if responsesOutputIsTerminal(out) {
			g.emitted = true
		}
		if bytes.HasPrefix(out, []byte("event:")) {
			_, _ = c.Writer.Write([]byte("\n"))
		}
		_, _ = c.Writer.Write(out)
		_, _ = c.Writer.Write([]byte("\n"))
	}
}

// ensure writes a terminal error event when nothing terminal was emitted, so
// the turn always ends with an event the client recognises.
func (g *responsesTerminalGuard) ensure(c *gin.Context, reason string) {
	if g.emitted {
		return
	}
	g.emitted = true
	body := handlersBuildResponsesErrorBody(reason)
	_, _ = fmt.Fprintf(c.Writer, "\nevent: error\ndata: %s\n\n", body)
}

// responsesOutputIsTerminal reports whether converter output carries a
// terminal response event.
func responsesOutputIsTerminal(out []byte) bool {
	for _, name := range [][]byte{
		[]byte("response.completed"),
		[]byte("response.incomplete"),
		[]byte("response.failed"),
		[]byte("response.error"),
	} {
		if bytes.Contains(out, name) {
			return true
		}
	}
	return bytes.HasPrefix(out, []byte("event: error"))
}

// handlersBuildResponsesErrorBody renders the error payload body.
func handlersBuildResponsesErrorBody(reason string) string {
	return fmt.Sprintf(`{"type":"error","sequence_number":0,"code":%d,"message":%q}`, http.StatusBadGateway, reason)
}
