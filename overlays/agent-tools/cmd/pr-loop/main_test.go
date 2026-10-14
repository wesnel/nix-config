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
		{"reply", "--from", "/etc/passwd"},
		{"reply", "--from=/etc/passwd"},
		{"reply", "-from", "-"},
		{"reply", "--from"},
		{"setup"},
		{"setup", "/x"},
	} {
		if relayRefusal(argv) == "" {
			t.Errorf("%v was accepted", argv)
		}
	}
	for _, argv := range [][]string{
		{"reply", "T", "--body-file", "-"},
		{"reply", "--from", "-"},
		{"status", "12"},
		{"open", "--title", "T", "--body-file", "-", "--base", "parent", "x"},
		{"setup-command"},
	} {
		if reason := relayRefusal(argv); reason != "" {
			t.Errorf("%v was refused: %s", argv, reason)
		}
	}
}
