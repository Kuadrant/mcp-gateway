package main

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestFirstOverLimit(t *testing.T) {
	tests := []struct {
		name    string
		measure func(int) int
		want    int
	}{
		{"first entry", func(n int) int { return n * (corev1.MaxSecretSize + 1) }, 1},
		{"exact limit is allowed", func(n int) int { return corev1.MaxSecretSize - 20 + n*10 }, 3},
		{"larger search", func(n int) int { return n * 1000 }, 1049},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := firstOverLimit(tt.measure); got != tt.want {
				t.Fatalf("first count over limit = %d, want %d", got, tt.want)
			}
		})
	}
}
