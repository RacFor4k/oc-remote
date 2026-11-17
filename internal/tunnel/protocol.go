package tunnel

// Frame is one JSON message on the device tunnel.
type Frame struct {
	T       string `json:"t"`
	ID      uint64 `json:"id,omitempty"`
	Device  string `json:"device,omitempty"`
	Tenant  string `json:"tenant,omitempty"`
	Version string `json:"version,omitempty"`

	Method  string      `json:"method,omitempty"`
	Path    string      `json:"path,omitempty"`
	Headers [][2]string `json:"headers,omitempty"`
	Auth    string      `json:"auth,omitempty"`
	Body    []byte      `json:"body,omitempty"`

	Status int    `json:"status,omitempty"`
	Code   string `json:"code,omitempty"`
	URL    string `json:"url,omitempty"`

	ExpiresIn int    `json:"expires_in,omitempty"`
	Kind      string `json:"kind,omitempty"`
	SessionID string `json:"sessionID,omitempty"`
	RequestID string `json:"requestID,omitempty"`
	Action    string `json:"action,omitempty"`

	Error string `json:"error,omitempty"`
}
