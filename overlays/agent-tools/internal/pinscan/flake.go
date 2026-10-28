package pinscan

import (
	"regexp"
	"slices"
	"strings"
)

// FlakeInputsChanged reports whether inputs may have changed. Only complete
// ordinary root sets with literal input declarations can establish equality;
// unsupported syntax on either side requires review. No Nix is evaluated.
func FlakeInputsChanged(old, next string) bool {
	a, okA := literalFlakeInputs(old)
	b, okB := literalFlakeInputs(next)
	return !okA || !okB || !slices.Equal(a, b)
}

var flakeAttr = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_'-]*$`)
var flakeInt = regexp.MustCompile(`^-?[0-9]+$`)

func literalFlakeInputs(text string) ([]string, bool) {
	l := flakeLexer{text: text}
	var ts []string
	for l.at < len(text) {
		t, ok := l.token(0)
		if !ok {
			return nil, false
		}
		if t != "" {
			ts = append(ts, t)
		}
	}
	p := flakeTokens{ts: ts}
	if !p.take("{") {
		return nil, false
	}
	var inputs, paths []string
	for !p.take("}") {
		start := p.at
		path, ok := p.path()
		if !ok || !uniqueFlakePath(&paths, path) || !p.take("=") {
			return nil, false
		}
		if path == "inputs" || strings.HasPrefix(path, "inputs.") {
			if !p.literal(0) || !p.take(";") {
				return nil, false
			}
			inputs = append(inputs, ts[start:p.at]...)
		} else if !p.expression() {
			return nil, false
		}
	}
	return inputs, p.at == len(ts)
}

func uniqueFlakePath(paths *[]string, path string) bool {
	for _, prev := range *paths {
		if prev == path || strings.HasPrefix(prev, path+".") || strings.HasPrefix(path, prev+".") {
			return false
		}
	}
	*paths = append(*paths, path)
	return true
}

type flakeTokens struct {
	ts []string
	at int
}

func (p *flakeTokens) take(t string) bool {
	if p.at == len(p.ts) || p.ts[p.at] != t {
		return false
	}
	p.at++
	return true
}

func (p *flakeTokens) path() (string, bool) {
	var parts []string
	for {
		if p.at == len(p.ts) || !flakeAttr.MatchString(p.ts[p.at]) ||
			slices.Contains([]string{"inherit", "rec", "let", "in", "with", "assert"}, p.ts[p.at]) {
			return "", false
		}
		parts = append(parts, p.ts[p.at])
		p.at++
		if !p.take(".") {
			return strings.Join(parts, "."), true
		}
	}
}

func (p *flakeTokens) literal(depth int) bool {
	if depth > 128 || p.at == len(p.ts) {
		return false
	}
	if p.take("{") {
		var paths []string
		for !p.take("}") {
			path, ok := p.path()
			if !ok || !uniqueFlakePath(&paths, path) || !p.take("=") || !p.literal(depth+1) || !p.take(";") {
				return false
			}
		}
		return true
	}
	t := p.ts[p.at]
	if (strings.HasPrefix(t, `"`) || strings.HasPrefix(t, "''")) && !strings.Contains(t, "${") ||
		t == "true" || t == "false" || t == "null" || flakeInt.MatchString(t) {
		p.at++
		return true
	}
	return false
}

// expression skips a non-input value, keeping its let/with/assert semicolons
// and balanced groups inside the value rather than treating them as bindings.
func (p *flakeTokens) expression() bool {
	var stack []string
	lets, guards, start := 0, 0, p.at
	for p.at < len(p.ts) {
		t := p.ts[p.at]
		p.at++
		switch t {
		case "{", "[", "(":
			stack = append(stack, map[string]string{"{": "}", "[": "]", "(": ")"}[t])
		case "}", "]", ")":
			if len(stack) == 0 || stack[len(stack)-1] != t {
				return false
			}
			stack = stack[:len(stack)-1]
		default:
			if len(stack) > 0 {
				continue
			}
			switch t {
			case "let":
				lets++
			case "in":
				if lets == 0 {
					return false
				}
				lets--
			case "with", "assert":
				guards++
			case ";":
				if guards > 0 {
					guards--
				} else if lets == 0 {
					return p.at > start+1
				}
			}
		}
	}
	return false
}

type flakeLexer struct {
	text string
	at   int
}

func (l *flakeLexer) token(depth int) (string, bool) {
	if depth > 128 {
		return "", false
	}
	for l.at < len(l.text) {
		c := l.text[l.at]
		switch {
		case strings.ContainsRune(" \t\r\n", rune(c)):
			l.at++
		case c == '#':
			for l.at < len(l.text) && l.text[l.at] != '\n' {
				l.at++
			}
		case strings.HasPrefix(l.text[l.at:], "/*"):
			end := strings.Index(l.text[l.at+2:], "*/")
			if end < 0 {
				return "", false
			}
			l.at += end + 4
		default:
			start := l.at
			if c == '"' || strings.HasPrefix(l.text[l.at:], "''") {
				if !l.quoted(depth + 1) {
					return "", false
				}
			} else if strings.ContainsRune("{}[]();=.", rune(c)) {
				l.at++
			} else {
				for l.at < len(l.text) && !strings.ContainsRune(" \t\r\n{}[]();=.\"#", rune(l.text[l.at])) &&
					!strings.HasPrefix(l.text[l.at:], "/*") && !strings.HasPrefix(l.text[l.at:], "''") {
					l.at++
				}
				// Inside a token these delimiters can belong to an identifier,
				// URI or path. Guessing a new lexical region could hide inputs.
				tail := l.text[l.at:]
				if strings.HasPrefix(tail, "''") || strings.HasPrefix(tail, "/*") ||
					strings.HasPrefix(tail, `"`) || strings.HasPrefix(tail, "#") {
					return "", false
				}
			}
			return l.text[start:l.at], l.at > start
		}
	}
	return "", true
}

func (l *flakeLexer) quoted(depth int) bool {
	if depth > 128 {
		return false
	}
	indented := strings.HasPrefix(l.text[l.at:], "''")
	l.at++
	if indented {
		l.at++
	}
	for l.at < len(l.text) {
		s := l.text[l.at:]
		switch {
		case strings.HasPrefix(s, "$${") || indented && strings.HasPrefix(s, "''\\"):
			return false
		case indented && (strings.HasPrefix(s, "'''") || strings.HasPrefix(s, "''$")):
			l.at += 3
		case indented && strings.HasPrefix(s, "''"):
			l.at += 2
			return true
		case !indented && s[0] == '\\':
			l.at += 2
		case !indented && s[0] == '"':
			l.at++
			return true
		case strings.HasPrefix(s, "${"):
			l.at += 2
			braces := 1
			for braces > 0 && l.at < len(l.text) {
				t, ok := l.token(depth + 1)
				if !ok {
					return false
				}
				if t == "{" {
					braces++
				} else if t == "}" {
					braces--
				}
			}
			if braces != 0 {
				return false
			}
		default:
			l.at++
		}
	}
	return false
}
