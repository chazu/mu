package scenario

// Bundled returns the mu-authored generic scenarios that apply to every
// plugin. These are pure Go values rather than embedded YAML so the
// binary is self-contained and the scenarios are easy to diff in code
// review.
func Bundled() []*Scenario {
	return []*Scenario{
		{
			Name:        "discover_ok",
			Description: "Plugin responds to discover with its identity and protocol version.",
			Request:     map[string]any{"method": "discover"},
			Expect: Expect{
				Match: "shape",
				Shape: map[string]any{
					"name":             "<string,non-empty>",
					"version":          "<string>",
					"protocol_version": "<int>",
				},
			},
		},
		{
			Name:        "discover_version_pin",
			Description: "Plugin's protocol_version is a valid integer (currently always 1).",
			Request:     map[string]any{"method": "discover"},
			Expect: Expect{
				Match: "shape",
				Shape: map[string]any{
					"protocol_version": float64(1),
				},
			},
		},
	}
}
