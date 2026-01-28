package main

import (
	"testing"
)

func TestGetDomain(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"example.com", "example.com"},
		{"sub.example.com", "example.com"},
		{"_acme-challenge.sub.example.com", "example.com"},
		{"example.com.", "example.com"},
		{"deep.nested.sub.example.co.uk", "example.co.uk"},
		{"example.co.uk", "example.co.uk"},
		{"test.example.org", "example.org"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := getDomain(tt.input)
			if got != tt.expected {
				t.Errorf("getDomain(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestGetSub(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"example.com", ""},
		{"sub.example.com", "sub"},
		{"_acme-challenge.sub.example.com", "_acme-challenge.sub"},
		{"example.com.", ""},
		{"deep.nested.example.com", "deep.nested"},
		{"_acme-challenge.example.com", "_acme-challenge"},
		{"a.b.c.example.co.uk", "a.b.c"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := getSub(tt.input)
			if got != tt.expected {
				t.Errorf("getSub(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}
