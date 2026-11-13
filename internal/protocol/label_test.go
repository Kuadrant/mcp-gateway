package protocol

import "testing"

func TestMetricLabel(t *testing.T) {
	cases := map[string]string{
		Version2026:  Version2026,
		Version2025:  Version2025,
		"2025-06-18": LabelOlder,
		"2025-03-26": LabelOlder,
		"2024-11-05": LabelOlder,
		"":           LabelUnknown,
		"2099-01-01": LabelUnknown,
		"garbage":    LabelUnknown,
	}
	for in, want := range cases {
		if got := MetricLabel(in); got != want {
			t.Errorf("MetricLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClientMetricLabel(t *testing.T) {
	cases := map[string]string{
		"":           LabelOlder,
		Version2026:  Version2026,
		"2099-01-01": LabelUnknown,
	}
	for in, want := range cases {
		if got := ClientMetricLabel(in); got != want {
			t.Errorf("ClientMetricLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMetricLabelIdempotent(t *testing.T) {
	for _, l := range MetricLabels() {
		if got := MetricLabel(l); got != l {
			t.Errorf("MetricLabel(%q) = %q, label values must map to themselves", l, got)
		}
	}
}

func TestMetricLabelsCoversAllOutputs(t *testing.T) {
	labels := map[string]bool{}
	for _, l := range MetricLabels() {
		labels[l] = true
	}
	for _, in := range []string{"", "x", "2024-11-05", Version2025, Version2026} {
		if !labels[MetricLabel(in)] || !labels[ClientMetricLabel(in)] {
			t.Errorf("label for %q not in MetricLabels()", in)
		}
	}
	if len(labels) != 4 {
		t.Errorf("expected 4 bounded labels, got %v", MetricLabels())
	}
}
