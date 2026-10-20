package redact

import (
	"errors"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/refusal"
)

// pemLine is a fake line of a PEM key's body.
var pemLine = strings.Repeat("Fa9+", 16)

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
		{"netrc line", "machine h login u password FAKEPW", "machine h login u password [REDACTED]"},
		{"netrc lines", "machine h\n  login u\n  password FAKEPW\n", "machine h\n  login u\n  password [REDACTED]\n"},
		{"Cookie header", "> Cookie: session=FAKE; theme=dark", "> Cookie: [REDACTED]"},
		{"Set-Cookie header", "< Set-Cookie: session=FAKE; Path=/", "< Set-Cookie: [REDACTED]"},
		{"quoted Cookie header", "curl -H 'Cookie: session=FAKE' x", "curl -H 'Cookie: [REDACTED]' x"},
		{"curl user", "curl -u bob:FAKEPW https://x", "curl -u bob:[REDACTED] https://x"},
		{"curl --user", "curl --user=bob:FAKEPW https://x", "curl --user=bob:[REDACTED] https://x"},
		{"mysql password", "mysql -ubob -pFAKEPW db", "mysql -ubob -p[REDACTED] db"},
		{"mysqldump password", "mysqldump -h x -pFAKEPW db", "mysqldump -h x -p[REDACTED] db"},
		{"OpenAI project key", "sk-proj-" + strings.Repeat("Fa9_", 10), "[REDACTED OpenAI key]"},
		{"npm token", "npm_" + strings.Repeat("Fa9", 12), "[REDACTED npm token]"},
		{"Hugging Face token", "hf_" + strings.Repeat("Fak", 12), "[REDACTED Hugging Face token]"},
		{"Grafana token", "glsa_" + strings.Repeat("Fa9", 10) + "Fa_0123abcd", "[REDACTED Grafana token]"},
		{"DigitalOcean token", "dop_v1_" + strings.Repeat("0f", 32), "[REDACTED DigitalOcean token]"},
		{"Google API key", "x AIzaSyA" + strings.Repeat("b-_9", 8) + " y", "x [REDACTED Google API key] y"},
		{"not a Google API key", "AIzaShort", "AIzaShort"},
		{"credentials variable", "MY_CREDENTIALS=FAKE rest", "MY_CREDENTIALS=[REDACTED] rest"},
		{"auth variable", "AUTH=FAKE rest", "AUTH=[REDACTED] rest"},
		{"npm auth", "//registry/:_auth=FAKE", "//registry/:_auth=[REDACTED]"},
		{"JSON credentials object", `{"credentials":{"value":"FAKE"},"n":1}`, `{"credentials":[REDACTED],"n":1}`},
		{"PGP private key", "-----BEGIN PGP PRIVATE KEY BLOCK-----\n\nlQOYBF\n=ab12\n-----END PGP PRIVATE KEY BLOCK-----\nafter",
			"[REDACTED private key]\nafter"},
		{"PEM cut short", "before\n-----BEGIN RSA PRIVATE KEY-----\nMIIEFAKE\nMIIEFAKE", "before\n[REDACTED private key]"},
		{"PEM tail from mid-body", strings.Repeat(pemLine+"\n", 3) + "Fa9+/w==\n-----END PRIVATE KEY-----\nafter",
			strings.Repeat("[REDACTED key line]\n", 4) + "[REDACTED private key]\nafter"},
		{"PEM body cut at both ends", "out\n" + strings.Repeat(pemLine+"\n", 2), "out\n" + strings.Repeat("[REDACTED key line]\n", 2)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Secrets(tc.in); got != tc.want {
				t.Errorf("Secrets(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
	for _, plain := range []string{"Add the run loop", "the KEY is in the vault", "eyJ alone", "ghp_short",
		"commit 0123456789abcdef0123456789abcdef01234567", "routeKey := x", "https://github.com/o/r.git",
		"--key-file path", "the bearer of news", "the password is in the vault", "sort -u names", "git push -u origin main",
		"mysql -p db", "author: the operator", "credential.helper=store", "sk-ant-short", strings.Repeat("ab12", 20),
		"go test ./internal/" + strings.Repeat("Ab1", 20) + " ok"} {
		if got := Secrets(plain); got != plain {
			t.Errorf("Secrets(%q) = %q, want it unchanged", plain, got)
		}
	}
}

// Text an agent posts is refused while it holds a secret's shape, and the
// refusal names the kind but never the secret.
func TestOutgoing(t *testing.T) {
	token := "ghp_" + strings.Repeat("a", 36)
	for _, tc := range []struct{ name, in, kind string }{
		{"GitHub token", "Fixed; the token " + token + " is gone", "GitHub token"},
		{"named value", "Set PASSWORD=hunter2 first", "secret given by name"},
		{"two kinds", "AKIA" + strings.Repeat("Z", 16) + " and " + token, "AWS key and a GitHub token"},
		{"a bearer token", "Send `Authorization: Bearer " + strings.Repeat("aB3dE6gH9j", 4) + "`", "secret given by name"},
		{"a placeholder beside a token", "Set NGROK_AUTHTOKEN=$NGROK_AUTHTOKEN, not " + token, "GitHub token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Outgoing(tc.in)
			var r refusal.Refusal
			switch {
			case !errors.As(err, &r):
				t.Fatalf("Outgoing(%q) = %v, want a refusal", tc.in, err)
			case !strings.Contains(r.Error(), tc.kind):
				t.Errorf("%q does not name %q", r.Error(), tc.kind)
			case strings.Contains(r.Error(), token) || strings.Contains(r.Error(), "hunter2"):
				t.Errorf("%q names the secret", r.Error())
			case !refusal.SaysInstead(r.Error()):
				t.Errorf("%q does not say what to do instead", r.Error())
			}
		})
	}
	for _, plain := range []string{"Fixed in 0123456789abcdef0123456789abcdef01234567.", "Already [REDACTED] upstream.", ""} {
		if err := Outgoing(plain); err != nil {
			t.Errorf("Outgoing(%q) = %v", plain, err)
		}
	}
}

// A placeholder where a secret's value would go passes Outgoing, since PR
// discussion is full of them, and Secrets still replaces it.
func TestOutgoingPlaceholders(t *testing.T) {
	for _, text := range []string{
		"`authtoken: abc123`",
		"NGROK_AUTHTOKEN=$NGROK_AUTHTOKEN",
		"`api_key: \"{{ .Values.apiKey }}\"`",
		"`Authorization: Bearer <token>`",
		"--password=$DB_PASSWORD",
		"export GITHUB_TOKEN=${GITHUB_TOKEN}",
		"postgres://app:$PGPASSWORD@db/app",
		"password: changeme, secret: EXAMPLE, token=xxxxxxxx, key: Redacted",
		"key: ${{ secrets.X }}",
		"password = \"<redacted>\"",
		"token: {{ .Values.token }} and {{ .Values.other }}",
		// A placeholder at the end of a sentence or a clause.
		"Send `Authorization: Bearer <token>`.",
		"Export GITHUB_TOKEN=$GITHUB_TOKEN.",
		"Set api_key: <key>, then run it.",
		"(Use token=${TOKEN}).",
	} {
		if err := Outgoing(text); err != nil {
			t.Errorf("Outgoing(%q) = %v", text, err)
		}
		if Secrets(text) == text {
			t.Errorf("Secrets(%q) left it unchanged", text)
		}
	}
	// A placeholder with anything beside it is a value, and a token of a
	// known shape is one however it is wrapped.
	token := "ghp_" + strings.Repeat("a", 36)
	random := strings.Repeat("aB3dE6gH9j", 4)
	for _, text := range []string{
		"token: <a>b",
		"Authorization: Bearer <token> " + random,
		"password=$(cat ~/.pw)",
		"api_key: abc1234",
		"password=\"changeme!\"",
		"password: (example)",
		"password: {{x}}hunter2Real{{y}}",
		"token: <" + token + ">",
		"{{" + token + "}}",
		"${{ " + token + " }}",
		"$" + token,
		"Authorization: Bearer {{" + random + "}}",
		"Authorization: Bearer <" + random + ">",
		"password = hunter2",
		"Use password: changeme.",
		"token: <" + token + ">.",
		"Export TOKEN=$" + token + ".",
		"Send `Authorization: Bearer " + random + "`.",
	} {
		if Outgoing(text) == nil {
			t.Errorf("Outgoing(%q) passed", text)
		}
	}
}
