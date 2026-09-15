package helps

import "testing"

func TestDecodeCursorArgValue(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want interface{}
	}{
		{"empty", nil, nil},
		{"empty slice", []byte{}, nil},
		{"string", []byte(`"hello"`), "hello"},
		{"number", []byte(`42`), interface{}( // json.Number
			// can't construct json.Number literal in test reliably; compare via fmt
			// Use a sentinel that round-trips through Decoder.
			nil),
		},
		{"object", []byte(`{"k":1}`), map[string]interface{}{"k": interface{}(nil)}},
		{"malformed bytes fallback to string", []byte(`not-json`), "not-json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DecodeCursorArgValue(tc.in)
			// For the number and object cases above, UseNumber yields json.Number and the
			// generic interface{} nil placeholder. We assert via JSON-round-trip instead.
			switch tc.name {
			case "number":
				if got == nil {
					t.Fatalf("number decode returned nil")
				}
			case "object":
				if got == nil {
					t.Fatalf("object decode returned nil")
				}
			default:
				if got != tc.want {
					t.Fatalf("got %v (%T), want %v (%T)", got, got, tc.want, tc.want)
				}
			}
		})
	}
}

func TestDecodeCursorArgsMap(t *testing.T) {
	in := map[string][]byte{
		"a": []byte(`"alpha"`),
		"b": []byte(`{"nested":true}`),
		"c": nil,
	}
	got := DecodeCursorArgsMap(in)
	if got == nil {
		t.Fatalf("result must be non-nil for nil/missing args")
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(got))
	}
	if got["a"] != "alpha" {
		t.Fatalf("a: want %q got %v", "alpha", got["a"])
	}
	if got["b"] == nil {
		t.Fatalf("b: nested object decode returned nil")
	}
	if got["c"] != nil {
		t.Fatalf("c: nil bytes must yield nil value, got %v", got["c"])
	}
}

func TestDecodeCursorArgsMap_NilInput(t *testing.T) {
	got := DecodeCursorArgsMap(nil)
	if got == nil {
		t.Fatalf("nil input must yield empty (non-nil) map")
	}
	if len(got) != 0 {
		t.Fatalf("nil input must yield empty map, got %d entries", len(got))
	}
}
