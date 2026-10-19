// Package redact takes known secret shapes out of text before it goes to
// a reviewer, whichever model that is. It knows shapes, not secrets: a
// secret of no shape here passes.
package redact

import (
	"regexp"
	"strings"
)

// shapes are the secrets replaced, in order, each with what replaces it;
// $1 keeps what comes before a value. The rules that go by a name come
// before the token shapes, so a token given a name is replaced once.
var shapes = []struct {
	re   *regexp.Regexp
	with string
}{
	// A private key, PEM or PGP, to its END line or, cut short, to the end
	// of the text; then an END line whose key began before the text did.
	{regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----.*?(?:-----END [A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----|\z)`),
		"[REDACTED private key]"},
	{regexp.MustCompile(`-----END [A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----`), "[REDACTED private key]"},
	{regexp.MustCompile(`(?i)(authorization:[ \t]*)[^\r\n]+`), "${1}[REDACTED]"},
	{regexp.MustCompile(`(?i)(\b(?:set-)?cookie:[ \t]*)[^\r\n'"]+`), "${1}[REDACTED]"},
	// A password in a URL: scheme://user:password@host.
	{regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://[^\s/:@]+:)[^\s/@]+@`), "${1}[REDACTED]@"},
	// curl's user and password: -u user:password, --user user:password.
	{regexp.MustCompile(`((?:^|[ \t])(?:-u[ \t]*|--user(?:[ \t]+|=))['"]?[^\s:'"]+:)[^\s'"]+`), "${1}[REDACTED]"},
	// The MySQL clients' password, attached to -p.
	{regexp.MustCompile(`(\b(?:mysql|mysqldump|mysqladmin|mariadb)\b[^\n;&|]*?[ \t]-p)\S+`), "${1}[REDACTED]"},
	// A netrc password, in a line that names its machine or login or alone
	// on its line.
	{regexp.MustCompile(`(?im)((?:^|\b(?:machine|login|default)\b[^\n]*?)[ \t]*\bpassword[ \t]+)\S+`), "${1}[REDACTED]"},
	// A value given a secret's name, in any case: NAME=value, name: value,
	// "name": "value", "name": {...}, --name=value. A value never starts
	// with = or :, so Go's `key := x` is left alone.
	{regexp.MustCompile(`(?i)(\b[a-z0-9_.-]*(?:token|key|secret|password|passwd|credentials|auth)"?[ \t]*[=:][ \t]*)` +
		`("[^"\n]*"|'[^'\n]*'|\{[^{}\n]*\}|[^\s"',;:=][^\s"',;]*)`), "${1}[REDACTED]"},
	// The same as a flag and its separate argument: --api-key value.
	{regexp.MustCompile(`(?i)(--[a-z0-9-]*(?:token|key|secret|password)[ \t]+)[^\s-]\S*`), "${1}[REDACTED]"},
	{regexp.MustCompile(`(?i)\b(bearer[ \t]+)[A-Za-z0-9._~+/=-]{12,}`), "${1}[REDACTED]"},
	{regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,255}\b`), "[REDACTED GitHub token]"},
	{regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{22,255}`), "[REDACTED GitHub token]"},
	{regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`), "[REDACTED AWS key]"},
	{regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}`), "[REDACTED Slack token]"},
	{regexp.MustCompile(`https://hooks\.slack\.com/services/[A-Za-z0-9/]+`), "[REDACTED Slack webhook]"},
	// An ngrok authtoken: a digit and 26 more, an underscore, and 17 or
	// more.
	{regexp.MustCompile(`\b[0-9][A-Za-z0-9]{26}_[A-Za-z0-9]{17,}\b`), "[REDACTED ngrok token]"},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{5,}\.eyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}`), "[REDACTED JWT]"},
	{regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}`), "[REDACTED Anthropic key]"},
	{regexp.MustCompile(`\b[rs]k_live_[A-Za-z0-9]{16,}`), "[REDACTED Stripe key]"},
	{regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}`), "[REDACTED GitLab token]"},
	{regexp.MustCompile(`\bbkua_[A-Za-z0-9]{20,}`), "[REDACTED Buildkite token]"},
	{regexp.MustCompile(`\blin_api_[A-Za-z0-9]{20,}`), "[REDACTED Linear key]"},
	{regexp.MustCompile(`\bsk-(?:proj|svcacct|admin)-[A-Za-z0-9_-]{20,}`), "[REDACTED OpenAI key]"},
	{regexp.MustCompile(`\bnpm_[A-Za-z0-9]{36,}`), "[REDACTED npm token]"},
	{regexp.MustCompile(`\bhf_[A-Za-z0-9]{30,}`), "[REDACTED Hugging Face token]"},
	{regexp.MustCompile(`\bglsa_[A-Za-z0-9]{32}_[0-9a-f]{8}\b`), "[REDACTED Grafana token]"},
	{regexp.MustCompile(`\bdo[por]_v1_[0-9a-f]{64}\b`), "[REDACTED DigitalOcean token]"},
	{regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}`), "[REDACTED Google API key]"},
}

// keyLine is a line of a key's base64 body, and keyEnd one of its padded
// last lines.
var (
	keyLine = regexp.MustCompile(`^[ \t]*[A-Za-z0-9+/]{60,}={0,2}[ \t\r]*$`)
	keyEnd  = regexp.MustCompile(`^[ \t]*[A-Za-z0-9+/]+={0,2}[ \t\r]*$`)
)

// keyLines is text with each line of a key's body that shows no BEGIN line,
// as a tail cut from a key's middle does, replaced: a long line of base64
// mixing cases and digits, and the shorter one after it.
func keyLines(text string) string {
	lines := strings.Split(text, "\n")
	changed, prev := false, false
	for i, l := range lines {
		long := keyLine.MatchString(l) && secretShaped(l)
		if long || prev && keyEnd.MatchString(l) {
			lines[i], changed = "[REDACTED key line]", true
		}
		prev = long
	}
	if !changed {
		return text
	}
	return strings.Join(lines, "\n")
}

// awsSecret is a run of the 40 characters an AWS secret access key is made
// of, with what borders it; secretShaped tells it from a commit hash.
var awsSecret = regexp.MustCompile(`(^|[^A-Za-z0-9/+=])([A-Za-z0-9/+]{40})($|[^A-Za-z0-9/+=])`)

// secretShaped reports whether s mixes upper and lower case letters and
// digits, as an AWS secret access key does and a hex commit hash or a
// word does not.
func secretShaped(s string) bool {
	return strings.ContainsAny(s, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") && strings.ContainsAny(s, "abcdefghijklmnopqrstuvwxyz") &&
		strings.ContainsAny(s, "0123456789")
}

// Secrets is text with each known secret shape replaced by a marker that
// names its kind.
func Secrets(text string) string {
	text = keyLines(text)
	for _, s := range shapes {
		text = s.re.ReplaceAllString(text, s.with)
	}
	return awsSecret.ReplaceAllStringFunc(text, func(m string) string {
		parts := awsSecret.FindStringSubmatch(m)
		if !secretShaped(parts[2]) {
			return m
		}
		return parts[1] + "[REDACTED AWS secret key]" + parts[3]
	})
}
