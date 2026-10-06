package executor

import (
	"bytes"
	"context"
	"strings"
	"testing"

	log "github.com/sirupsen/logrus"
)

func captureCapLog(t *testing.T, run func()) string {
	t.Helper()
	var buffer bytes.Buffer
	previousOut, previousLevel := log.StandardLogger().Out, log.GetLevel()
	log.SetOutput(&buffer)
	log.SetLevel(log.WarnLevel)
	defer func() {
		log.SetOutput(previousOut)
		log.SetLevel(previousLevel)
	}()
	run()
	return buffer.String()
}

func TestMaxTokensCapIsLogged(t *testing.T) {
	t.Run("zcode records requested and applied values", func(t *testing.T) {
		exec := NewZcodeExecutor(nil)
		out := captureCapLog(t, func() {
			exec.capMaxTokens(context.Background(), []byte(`{"max_tokens":999999}`), "glm-5.3-flash")
		})
		for _, want := range []string{"provider=zcode", "model=glm-5.3-flash", "requested_tokens=999999", "applied_tokens=131072"} {
			if !strings.Contains(out, want) {
				t.Fatalf("log missing %q: %s", want, out)
			}
		}
	})
	t.Run("commandcode records requested and applied values", func(t *testing.T) {
		out := captureCapLog(t, func() {
			logCommandCodeMaxTokensCap(context.Background(), "deepseek/deepseek-v4.1-flash", []byte(`{"max_tokens":1000000}`))
		})
		for _, want := range []string{"provider=commandcode", "requested_tokens=1000000", "applied_tokens=200000"} {
			if !strings.Contains(out, want) {
				t.Fatalf("log missing %q: %s", want, out)
			}
		}
	})
	t.Run("values within the limit are not logged", func(t *testing.T) {
		exec := NewZcodeExecutor(nil)
		out := captureCapLog(t, func() {
			exec.capMaxTokens(context.Background(), []byte(`{"max_tokens":4096}`), "glm-5.3-flash")
			logCommandCodeMaxTokensCap(context.Background(), "m", []byte(`{"max_tokens":200000}`))
		})
		if out != "" {
			t.Fatalf("unexpected log output: %s", out)
		}
	})
}
