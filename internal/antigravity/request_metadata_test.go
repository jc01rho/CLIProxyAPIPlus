package antigravity

import (
	"strconv"
	"testing"
)

func TestFnv1a64Signed(t *testing.T) {
	// Values verified against agy-request-metadata.ts / Node Buffer FNV-1a.
	cases := []struct {
		in   string
		want string
	}{
		{"", "-3750763034362895579"},
		{"project-1", "2656245455449127464"},
		{"file:///workspace", "8477896141531666355"},
	}
	for _, tc := range cases {
		if got := Fnv1a64Signed(tc.in); got != tc.want {
			t.Fatalf("Fnv1a64Signed(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBuildAntigravityHarnessUserAgentExact(t *testing.T) {
	got := BuildAntigravityHarnessUserAgent("1.1.5", "darwin", "arm64")
	want := "antigravity/cli/1.1.5 (aidev_client; os_type=darwin; arch=arm64; auth_method=consumer)"
	if got != want {
		t.Fatalf("UA = %q, want %q", got, want)
	}
	gotX64 := BuildAntigravityHarnessUserAgent("1.1.5", "linux", "x64")
	wantX64 := "antigravity/cli/1.1.5 (aidev_client; os_type=linux; arch=amd64; auth_method=consumer)"
	if gotX64 != wantX64 {
		t.Fatalf("UA x64 = %q, want %q", gotX64, wantX64)
	}
}

func TestBuildAgyCLIHeaderPairsStreamExact(t *testing.T) {
	pairs, err := BuildAgyCLIHeaderPairs(
		"https://daily-cloudcode-pa.googleapis.com/v1internal:streamGenerateContent?alt=sse",
		AgyRequestInit{
			Headers: map[string]string{
				"User-Agent":    "antigravity/cli/1.1.5 (aidev_client; os_type=darwin; arch=arm64; auth_method=consumer)",
				"Authorization": "Bearer test-token",
			},
			Body: []byte(`{"project":"p"}`),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []HeaderPair{
		{Key: "Host", Value: "daily-cloudcode-pa.googleapis.com"},
		{Key: "User-Agent", Value: "antigravity/cli/1.1.5 (aidev_client; os_type=darwin; arch=arm64; auth_method=consumer)"},
		{Key: "Transfer-Encoding", Value: "chunked"},
		{Key: "Authorization", Value: "Bearer test-token"},
		{Key: "Content-Type", Value: "application/json"},
		{Key: "Accept-Encoding", Value: "gzip"},
	}
	if len(pairs) != len(want) {
		t.Fatalf("header count = %d, want %d: %+v", len(pairs), len(want), pairs)
	}
	for i := range want {
		if pairs[i] != want[i] {
			t.Fatalf("header[%d] = %+v, want %+v", i, pairs[i], want[i])
		}
	}
}

func TestApplyAgyAgentWireMetadataExactBody(t *testing.T) {
	// Minimal agent request payload; contents has 2 parts total → lastStepIndex=2.
	input := []byte(`{
		"project":"rising-fact-p41fc",
		"model":"gemini-3.5-flash-low",
		"userAgent":"antigravity",
		"requestType":"agent",
		"request":{
			"generationConfig":{"temperature":0},
			"contents":[
				{"role":"user","parts":[{"text":"hi"}]},
				{"role":"model","parts":[{"text":"yo"}]}
			],
			"systemInstruction":{"parts":[{"text":"sys"}]}
		}
	}`)
	session := &AgyRequestSessionContext{
		ConversationID:   "11111111-1111-1111-1111-111111111111",
		TrajectoryID:     "22222222-2222-2222-2222-222222222222",
		NumericSessionID: Fnv1a64Signed("rising-fact-p41fc"),
	}
	const ts int64 = 1720000000000
	got := string(ApplyAgyAgentWireMetadata(input, session, "gemini-3.5-flash-low", ts))

	// Exact raw body string — field order + values.
	want := `{"project":"rising-fact-p41fc","requestId":"agent/11111111-1111-1111-1111-111111111111/1720000000000/22222222-2222-2222-2222-222222222222/3","request":{"contents":[{"role":"user","parts":[{"text":"hi"}]},{"role":"model","parts":[{"text":"yo"}]}],"systemInstruction":{"parts":[{"text":"sys"}]},"labels":{"last_step_index":"2","model_enum":"MODEL_PLACEHOLDER_M20","trajectory_id":"22222222-2222-2222-2222-222222222222","used_claude":"false","used_claude_conservative":"false","used_non_gemini_model":"false"},"generationConfig":{"temperature":0},"sessionId":"` + Fnv1a64Signed("rising-fact-p41fc") + `"},"model":"gemini-3.5-flash-low","userAgent":"antigravity","requestType":"agent"}`
	if got != want {
		t.Fatalf("body mismatch\ngot:  %s\nwant: %s", got, want)
	}
}

func TestApplyAgyAgentWireMetadataClaudeLabels(t *testing.T) {
	input := []byte(`{
		"project":"p1",
		"model":"claude-sonnet-4-6",
		"userAgent":"antigravity",
		"requestType":"agent",
		"request":{"contents":[{"role":"user","parts":[{"text":"a"}]}]}
	}`)
	session := &AgyRequestSessionContext{
		ConversationID:   "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		TrajectoryID:     "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
		NumericSessionID: Fnv1a64Signed("p1"),
	}
	got := string(ApplyAgyAgentWireMetadata(input, session, "claude-sonnet-4-6", 1))
	wantID := "agent/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa/1/bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb/2"
	want := `{"project":"p1","requestId":"` + wantID + `","request":{"contents":[{"role":"user","parts":[{"text":"a"}]}],"labels":{"last_step_index":"1","model_enum":"MODEL_PLACEHOLDER_M35","trajectory_id":"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb","used_claude":"true","used_claude_conservative":"true","used_non_gemini_model":"true"},"sessionId":"` + Fnv1a64Signed("p1") + `"},"model":"claude-sonnet-4-6","userAgent":"antigravity","requestType":"agent"}`
	if got != want {
		t.Fatalf("claude body mismatch\ngot:  %s\nwant: %s", got, want)
	}
}

func TestBeginAgyRequestReusesSessionAndAccumulates(t *testing.T) {
	agySessionMu.Lock()
	agySessions = map[string]*agySessionEntry{}
	agySessionMu.Unlock()

	var uuidN int
	newUUID := func() string {
		uuidN++
		return "uuid-" + strconv.Itoa(uuidN)
	}
	numeric := Fnv1a64Signed("proj")

	// First turn (gemini model): fresh ids, ts honors nowMs.
	s1, ts1 := BeginAgyRequest("conv-A", numeric, 1000, newUUID)
	if s1.ConversationID != "uuid-1" || s1.TrajectoryID != "uuid-2" {
		t.Fatalf("first ids = %q/%q", s1.ConversationID, s1.TrajectoryID)
	}
	if ts1 != 1000 {
		t.Fatalf("ts1 = %d, want 1000", ts1)
	}
	meta1 := BuildAgyAgentRequestMetadata(s1, []byte(`{"contents":[{"parts":[{"text":"hi"}]}]}`), "gemini-3.5-flash-low", ts1)
	if meta1.Labels["used_claude"] != "false" || meta1.Labels["used_non_gemini_model"] != "false" {
		t.Fatalf("turn1 labels = %+v", meta1.Labels)
	}

	// Second turn same conversation, same wall-clock ms → monotonic ts, SAME ids.
	s2, ts2 := BeginAgyRequest("conv-A", numeric, 1000, newUUID)
	if s2 != s1 {
		t.Fatalf("expected same session pointer reused")
	}
	if ts2 != 1001 {
		t.Fatalf("ts2 = %d, want monotonic 1001", ts2)
	}

	// Claude turn on the same conversation accumulates used_* flags.
	meta2 := BuildAgyAgentRequestMetadata(s2, []byte(`{"contents":[{"parts":[{"text":"a"}]}]}`), "claude-sonnet-4-6", ts2)
	if meta2.Labels["used_claude"] != "true" || meta2.Labels["used_non_gemini_model"] != "true" {
		t.Fatalf("turn2 claude labels = %+v", meta2.Labels)
	}
	// Next gemini turn must STILL report accumulated used_* = true (sticky).
	s3, ts3 := BeginAgyRequest("conv-A", numeric, 1000, newUUID)
	meta3 := BuildAgyAgentRequestMetadata(s3, []byte(`{"contents":[{"parts":[{"text":"b"}]}]}`), "gemini-3.5-flash-low", ts3)
	if meta3.Labels["used_claude"] != "true" || meta3.Labels["used_non_gemini_model"] != "true" {
		t.Fatalf("turn3 sticky labels = %+v", meta3.Labels)
	}

	// A different conversation is isolated: fresh ids, non-accumulated flags.
	sB, _ := BeginAgyRequest("conv-B", numeric, 2000, newUUID)
	if sB == s1 || sB.ConversationID == s1.ConversationID {
		t.Fatalf("conv-B must be isolated from conv-A")
	}
	metaB := BuildAgyAgentRequestMetadata(sB, []byte(`{"contents":[{"parts":[{"text":"x"}]}]}`), "gemini-3.5-flash-low", 2000)
	if metaB.Labels["used_claude"] != "false" {
		t.Fatalf("conv-B should not inherit used_claude: %+v", metaB.Labels)
	}
}

func TestCountAgyRequestStepsMinOne(t *testing.T) {
	if got := CountAgyRequestSteps([]byte(`{}`)); got != 1 {
		t.Fatalf("empty = %d, want 1", got)
	}
	if got := CountAgyRequestSteps([]byte(`{"contents":[]}`)); got != 1 {
		t.Fatalf("empty contents = %d, want 1", got)
	}
	if got := CountAgyRequestSteps([]byte(`{"contents":[{"parts":[{"text":"a"},{"text":"b"}]},{"parts":[{"text":"c"}]}]}`)); got != 3 {
		t.Fatalf("3 parts = %d, want 3", got)
	}
}
