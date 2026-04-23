// Exercises numeric drift in Target.Config (map[string]any):
//   - int_val: 42 decodes to int64
//   - float_val: 3.5 decodes to float64
// See cue_decoder.go doc comment on numeric type drift.
targets: [
	{
		target:    "//nums"
		toolchain: "go"
		sources: []
		config: {
			int_val:   42
			float_val: 3.5
			str_val:   "hi"
			bool_val:  true
		}
	},
]
