package upstream

import (
	"slices"
	"testing"
)

// TestResolveSupportedVersions covers the override/captured/negotiated
// precedence, the union of a differing negotiated version, and empty inputs.
func TestResolveSupportedVersions(t *testing.T) {
	tests := []struct {
		name       string
		override   []string
		captured   []string
		negotiated string
		want       []string
	}{
		{
			name:       "override wins over captured and includes negotiated",
			override:   []string{"2025-11-25", "2026-07-28"},
			captured:   []string{"2026-07-28"},
			negotiated: "2026-07-28",
			want:       []string{"2025-11-25", "2026-07-28"},
		},
		{
			name:       "override missing negotiated appends it",
			override:   []string{"2025-11-25"},
			captured:   nil,
			negotiated: "2026-07-28",
			want:       []string{"2025-11-25", "2026-07-28"},
		},
		{
			name:       "no override falls back to captured plus negotiated",
			override:   nil,
			captured:   []string{"2026-07-28"},
			negotiated: "2026-07-28",
			want:       []string{"2026-07-28"},
		},
		{
			name:       "captured differing from negotiated appends negotiated",
			override:   nil,
			captured:   []string{"2025-11-25"},
			negotiated: "2026-07-28",
			want:       []string{"2025-11-25", "2026-07-28"},
		},
		{
			name:       "override differing from negotiated appends negotiated",
			override:   []string{"2026-07-28"},
			captured:   []string{"2026-07-28"},
			negotiated: "2025-11-25",
			want:       []string{"2026-07-28", "2025-11-25"},
		},
		{
			name:       "no override no captured yields negotiated only",
			override:   nil,
			captured:   nil,
			negotiated: "2025-11-25",
			want:       []string{"2025-11-25"},
		},
		{
			name:       "empty negotiated is not appended",
			override:   []string{"2025-11-25"},
			captured:   nil,
			negotiated: "",
			want:       []string{"2025-11-25"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveSupportedVersions(tt.override, tt.captured, tt.negotiated)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("resolveSupportedVersions(%v, %v, %q) = %v, want %v",
					tt.override, tt.captured, tt.negotiated, got, tt.want)
			}
		})
	}
}

// TestResolveSupportedVersions_NoInputAliasing guards against the returned
// slice sharing a backing array with the override input, which the append of
// the negotiated version could otherwise mutate.
func TestResolveSupportedVersions_NoInputAliasing(t *testing.T) {
	override := []string{"2025-11-25"}
	got := resolveSupportedVersions(override, nil, "2026-07-28")
	if len(override) != 1 || override[0] != "2025-11-25" {
		t.Fatalf("override mutated: %v", override)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 resolved versions, got %v", got)
	}
}
