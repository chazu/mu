package main

import "testing"

func TestCheckMuNamespace(t *testing.T) {
	cases := []struct {
		plugin string
		module string
		ok     bool
	}{
		{"aws", "mu/aws", true},
		{"aws", "mu/aws/sub", true},
		{"aws", "mu/k8s", false},
		{"aws", "mu/aws-extra", false}, // distinct segment, even if similar
		{"aws", "pudl/core", true},     // not mu/ namespace, not policed
		{"aws", "third/party", true},
		{"aws", "mu/", false}, // empty segment never matches a real plugin name
	}
	for _, tc := range cases {
		_, ok := checkMuNamespace(tc.plugin, tc.module)
		if ok != tc.ok {
			t.Errorf("checkMuNamespace(%q, %q) ok = %v, want %v", tc.plugin, tc.module, ok, tc.ok)
		}
	}
}
