package daemon

import (
	"encoding/json"
	"testing"
)

func TestDecodeRuntimeAutonomyMode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  json.RawMessage
		want string
	}{
		{name: "empty", want: runtimeAutonomySupervised},
		{name: "malformed", raw: json.RawMessage(`{`), want: runtimeAutonomySupervised},
		{name: "missing", raw: json.RawMessage(`{"gateway":{"mode":"local"}}`), want: runtimeAutonomySupervised},
		{name: "unknown", raw: json.RawMessage(`{"autonomy_mode":"root"}`), want: runtimeAutonomySupervised},
		{name: "supervised", raw: json.RawMessage(`{"autonomy_mode":"supervised"}`), want: runtimeAutonomySupervised},
		{name: "full access", raw: json.RawMessage(`{"autonomy_mode":"full-access"}`), want: runtimeAutonomyFullAccess},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := decodeRuntimeAutonomyMode(tc.raw, nil); got != tc.want {
				t.Fatalf("decodeRuntimeAutonomyMode() = %q, want %q", got, tc.want)
			}
		})
	}
}
