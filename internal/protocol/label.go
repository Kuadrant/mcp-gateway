package protocol

// metric label values besides Version2025 and Version2026
const (
	// LabelOlder groups stateful versions before Version2025.
	LabelOlder = "older"
	// LabelUnknown is any version the gateway does not support.
	LabelUnknown = "unknown"
)

// DefaultClientVersion is assumed for clients that declare no version,
// matching the SDK and broker default.
const DefaultClientVersion = "2025-03-26"

// olderVersions are the supported versions reported as LabelOlder.
var olderVersions = []string{"2024-11-05", DefaultClientVersion, "2025-06-18"}

// MetricLabel maps a protocol version to a bounded metric label value. label
// values map to themselves so already-bounded series can be re-bounded.
func MetricLabel(v string) string {
	switch v {
	case Version2026, Version2025, LabelOlder:
		return v
	}
	for _, o := range olderVersions {
		if v == o {
			return LabelOlder
		}
	}
	return LabelUnknown
}

// ClientMetricLabel is MetricLabel for a client-declared version, counting an
// absent version as DefaultClientVersion.
func ClientMetricLabel(v string) string {
	if v == "" {
		v = DefaultClientVersion
	}
	return MetricLabel(v)
}

// MetricLabels returns every value MetricLabel and ClientMetricLabel can produce.
func MetricLabels() []string {
	return []string{Version2026, Version2025, LabelOlder, LabelUnknown}
}
