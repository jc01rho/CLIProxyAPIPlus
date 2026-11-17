package helps

import (
	"strings"
	"testing"

	"github.com/tiktoken-go/tokenizer"
)

type recordingCodec struct {
	maxInput int
	ratio    int
}

func (c *recordingCodec) GetName() string { return "recording" }

func (c *recordingCodec) Count(in string) (int, error) {
	if len(in) > c.maxInput {
		c.maxInput = len(in)
	}
	return len(in) / c.ratio, nil
}

func (c *recordingCodec) Encode(string) ([]uint, []string, error) { return nil, nil, nil }

func (c *recordingCodec) Decode([]uint) (string, error) { return "", nil }

var _ tokenizer.Codec = (*recordingCodec)(nil)

func TestCountOpenAIChatTokensBoundsTokenizerInput(t *testing.T) {
	enc := &recordingCodec{ratio: 4}
	body := strings.Repeat("x", 512<<10)
	count, err := CountOpenAIChatTokens(enc, []byte(`{"input":"`+body+`"}`))
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if enc.maxInput > maxTokenizedBytes {
		t.Fatalf("encoder saw %d bytes, want at most %d", enc.maxInput, maxTokenizedBytes)
	}
	if want := int64(len(body) / enc.ratio); count != want {
		t.Fatalf("count = %d, want %d", count, want)
	}
}

func TestCountOpenAIChatTokensCountsSmallPayloadsWhole(t *testing.T) {
	enc := &recordingCodec{ratio: 4}
	body := strings.Repeat("y", 1024)
	count, err := CountOpenAIChatTokens(enc, []byte(`{"input":"`+body+`"}`))
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if enc.maxInput != len(body) {
		t.Fatalf("encoder saw %d bytes, want the whole %d-byte segment", enc.maxInput, len(body))
	}
	if want := int64(len(body) / enc.ratio); count != want {
		t.Fatalf("count = %d, want %d", count, want)
	}
}
