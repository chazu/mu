// Exercises *bool tri-state on CacheBackend.Read / Write:
//   - backend 0: neither field set → both nil
//   - backend 1: write: false     → Read nil, Write *false
//   - backend 2: read: true, write: false
cache: backends: [
	{type: "disk", path: "/tmp/a"},
	{type: "oci", registry: "r1", write:  false},
	{type: "oci", registry: "r2", read:   true, write: false},
]
