package handler

import "testing"

func TestValidateAgentRuntimeConfigAutonomyMode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		cfg     any
		wantErr bool
	}{
		{name: "nil"},
		{name: "missing", cfg: map[string]any{"gateway": map[string]any{"mode": "local"}}},
		{name: "supervised", cfg: map[string]any{"autonomy_mode": "supervised"}},
		{name: "full access", cfg: map[string]any{"autonomy_mode": "full-access"}},
		{name: "invalid", cfg: map[string]any{"autonomy_mode": "yolo"}, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAgentRuntimeConfig(tc.cfg)
			if tc.wantErr && err == nil {
				t.Fatal("expected error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
