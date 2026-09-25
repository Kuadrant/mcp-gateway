package upstream

import "testing"

// TestResolveCacheScope covers override precedence and the advertised fallback.
func TestResolveCacheScope(t *testing.T) {
	tests := []struct {
		name       string
		override   string
		advertised string
		want       string
	}{
		{
			name:       "override wins over advertised",
			override:   CacheScopePublic,
			advertised: CacheScopePrivate,
			want:       CacheScopePublic,
		},
		{
			name:       "override private wins over advertised public",
			override:   CacheScopePrivate,
			advertised: CacheScopePublic,
			want:       CacheScopePrivate,
		},
		{
			name:       "no override falls back to advertised",
			override:   "",
			advertised: CacheScopePrivate,
			want:       CacheScopePrivate,
		},
		{
			name:       "no override and empty advertised yields empty",
			override:   "",
			advertised: "",
			want:       "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveCacheScope(tt.override, tt.advertised); got != tt.want {
				t.Errorf("resolveCacheScope(%q, %q) = %q, want %q", tt.override, tt.advertised, got, tt.want)
			}
		})
	}
}
