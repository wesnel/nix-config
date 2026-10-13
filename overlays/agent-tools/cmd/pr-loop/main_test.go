package main

import "testing"

func TestRelayRefusal(t *testing.T) {
	for _, argv := range [][]string{
		{},
		{"sign"},
		{"end"},
		{"guard"},
		{"reply", "T", "--body-file", "/etc/passwd"},
		{"reply", "T", "--body-file=/etc/passwd"},
		{"reply", "T", "-body-file", "-"},
		{"reply", "T", "--body-file"},
	} {
		if relayRefusal(argv) == "" {
			t.Errorf("%v was accepted", argv)
		}
	}
	for _, argv := range [][]string{
		{"reply", "T", "--body-file", "-"},
		{"status", "12"},
	} {
		if reason := relayRefusal(argv); reason != "" {
			t.Errorf("%v was refused: %s", argv, reason)
		}
	}
}
