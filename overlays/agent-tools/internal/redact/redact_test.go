package redact

import (
	"strings"
	"testing"
)

// Known secret shapes are replaced before text reaches a reviewer; the
// words around them stay.
func TestSecrets(t *testing.T) {
	aws := "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	for _, tc := range []struct{ name, in, want string }{
		{"GitHub token", "token ghp_" + strings.Repeat("a", 36) + " here", "token [REDACTED GitHub token] here"},
		{"GitHub fine-grained token", "github_pat_" + strings.Repeat("A1_", 20), "[REDACTED GitHub token]"},
		{"AWS key", "key AKIA" + strings.Repeat("Z", 16), "key [REDACTED AWS key]"},
		{"AWS secret key", "secret " + aws + " end", "secret [REDACTED AWS secret key] end"},
		{"Slack token", "xoxb-1234567890-abcdefghij", "[REDACTED Slack token]"},
		{"ngrok authtoken", "2" + strings.Repeat("a", 26) + "_" + strings.Repeat("B", 21), "[REDACTED ngrok token]"},
		{"JWT", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2lnbmF0dXJlXzEyMw", "[REDACTED JWT]"},
		{"PEM block", "-----BEGIN OPENSSH PRIVATE KEY-----\nabc\ndef\n-----END OPENSSH PRIVATE KEY-----", "[REDACTED private key]"},
		{"Anthropic key", "sk-ant-api03-" + strings.Repeat("x1", 20), "[REDACTED Anthropic key]"},
		{"Stripe key", "sk_live_" + strings.Repeat("Ab1", 8), "[REDACTED Stripe key]"},
		{"GitLab token", "glpat-" + strings.Repeat("y2", 10), "[REDACTED GitLab token]"},
		{"Buildkite token", "bkua_" + strings.Repeat("0f", 20), "[REDACTED Buildkite token]"},
		{"Linear key", "lin_api_" + strings.Repeat("Zz9", 10), "[REDACTED Linear key]"},
		{"Authorization header", "Authorization: Bearer abc.def", "Authorization: [REDACTED]"},
		{"bare Bearer", "curl -H x Bearer abcdef123456 rest", "curl -H x Bearer [REDACTED] rest"},
		{"credentials in a URL", "clone https://user:pw123@github.com/o/r.git", "clone https://user:[REDACTED]@github.com/o/r.git"},
		{"token variable", "NGROK_AUTHTOKEN_TOKEN=s3cret rest", "NGROK_AUTHTOKEN_TOKEN=[REDACTED] rest"},
		{"key variable", "export API_KEY='x y'", "export API_KEY=[REDACTED]"},
		{"named token", "GITHUB_TOKEN=ghp_" + strings.Repeat("a", 36), "GITHUB_TOKEN=[REDACTED]"},
		{"JSON key", `{"api_key": "abc123", "user": "x"}`, `{"api_key": [REDACTED], "user": "x"}`},
		{"JSON password", `{"password":"hunter2"}`, `{"password":[REDACTED]}`},
		{"YAML", "password: hunter2", "password: [REDACTED]"},
		{"lowercase name", "aws_secret_access_key = " + aws, "aws_secret_access_key = [REDACTED]"},
		{"flag with =", "--token=abc123 --verbose", "--token=[REDACTED] --verbose"},
		{"flag and argument", "--api-key abc123 --verbose", "--api-key [REDACTED] --verbose"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Secrets(tc.in); got != tc.want {
				t.Errorf("Secrets(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
	for _, plain := range []string{"Add the run loop", "the KEY is in the vault", "eyJ alone", "ghp_short",
		"commit 0123456789abcdef0123456789abcdef01234567", "routeKey := x", "https://github.com/o/r.git",
		"--key-file path", "the bearer of news"} {
		if got := Secrets(plain); got != plain {
			t.Errorf("Secrets(%q) = %q, want it unchanged", plain, got)
		}
	}
}
