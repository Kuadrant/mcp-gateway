package controller

// ListenerConfig holds configuration extracted from a Gateway listener.
// This is an internal type not exposed via CRD.
type ListenerConfig struct {
	// port is the port number from the Gateway listener
	Port uint32 `json:"port,omitempty"`
	// hostname is the hostname from the Gateway listener (may be empty or a wildcard)
	Hostname string `json:"hostname,omitempty"`
	// name is the listener name (sectionName)
	Name string `json:"name,omitempty"`
	// protocol is the Gateway listener protocol (e.g. HTTP, HTTPS).
	// Used to determine whether the broker-router hairpin URL should use
	// http:// or https://.
	Protocol string `json:"protocol,omitempty"`
}
