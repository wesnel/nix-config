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
		{"open", "--title", "T", "--body-file", "/etc/passwd", "x"},
	} {
		if relayRefusal(argv) == "" {
			t.Errorf("%v was accepted", argv)
		}
	}
	for _, argv := range [][]string{
		{"reply", "T", "--body-file", "-"},
		{"status", "12"},
		{"open", "--title", "T", "--body-file", "-", "--base", "parent", "x"},
	} {
		if reason := relayRefusal(argv); reason != "" {
			t.Errorf("%v was refused: %s", argv, reason)
		}
	}
}
