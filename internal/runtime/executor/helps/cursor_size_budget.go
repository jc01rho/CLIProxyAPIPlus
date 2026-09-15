package helps

// Cursor envelope / wire size budgets (ported from opencodex/src/adapters/cursor/protobuf-request.ts).
//
// Cursor rejects oversized root replay sets with a late invalid_argument after hydrating every
// blob, and a single oversized argument can consume the entire history budget and truncate
// legitimate tool-result output. These constants define the byte ceilings we enforce in
// CPA's Cursor executor before serializing toward the wire.
//
// CPA does not generate protobuf directly today; we keep the values in Go so the executor
// can apply them when shaping JSON envelopes and tool-result payloads, and any future
// protobuf path can reuse the same numbers.

const (
	// CursorExternalRootBlobLimit caps the number of root replay blobs sent to the Cursor
	// upstream. Mirrors opencodex `CURSOR_EXTERNAL_ROOT_BLOB_LIMIT = 192`. Cursor IDE
	// similarly bounds / compacts long conversations rather than replaying an unbounded
	// message list.
	CursorExternalRootBlobLimit = 192

	// CursorExternalRootByteLimit caps the serialized byte size of the entire root replay
	// set. Mirrors opencodex `CURSOR_EXTERNAL_ROOT_BYTE_LIMIT = 512 * 1024` (512 KiB).
	// Tool schemas and protocol framing consume context separately, so this number is an
	// approximation rather than an exact wire-cap.
	CursorExternalRootByteLimit = 512 * 1024

	// CursorInvocationArgumentsByteLimit caps the serialized byte size of the arguments
	// payload named inside ONE replayed tool-result envelope. Mirrors opencodex
	// `CURSOR_INVOCATION_ARGUMENTS_BYTE_LIMIT = 2 * 1024` (2 KiB). Without this independent
	// cap a single large-but-legitimate argument (a 600 KiB file write) can consume the
	// whole root history budget and the result output is truncated away instead.
	CursorInvocationArgumentsByteLimit = 2 * 1024

	// CursorDefaultMaxClientToolCalls caps how many client tool-call records we retain
	// during one upstream turn. Mirrors opencodex `DEFAULT_MAX_CLIENT_TOOL_CALLS = 330`.
	// Once exceeded the oldest entries are evicted to keep memory bounded.
	CursorDefaultMaxClientToolCalls = 330
)
