package redcheck

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"
)

type rustModule struct{ start, end int }

// RustTestContent returns the exact trailing test module, or an empty slice
// when absent. It shares projection's lexical checks; unsupported boundaries
// return an error. The returned bytes refer to source.
func RustTestContent(source []byte) ([]byte, error) {
	module, err := rustTestModule(source)
	if err != nil {
		return nil, err
	}
	return source[module.start:module.end], nil
}

// rustTestModule recognizes one trailing top-level #[cfg(test)] module.
// Offsets exclude surrounding bytes, which belong to the parent even when
// they are whitespace or comments. Unknown layouts refuse rather than copy
// production code from the tested commit.
func rustTestModule(source []byte) (rustModule, error) {
	tokens, err := rustTokens(source)
	if err != nil {
		return rustModule{}, err
	}
	module := rustModule{len(source), len(source)}
	for i := 0; i < len(tokens); i++ {
		if !rustMatch(tokens[i:], "#", "[", "cfg", "(", "test", ")", "]") {
			continue
		}
		if tokens[i].depth != 0 || i > 0 && tokens[i-1].text != ";" && tokens[i-1].text != "}" {
			return module, fmt.Errorf("cfg(test) is not an unadorned top-level module")
		}
		j := i + 7
		if len(tokens)-j < 3 || tokens[j].text != "mod" || !rustName(tokens[j+1].text) || tokens[j+2].text != "{" {
			return module, fmt.Errorf("cfg(test) must annotate an inline module")
		}
		end := j + 3
		for end < len(tokens) && tokens[end].depth != 0 {
			end++
		}
		if end != len(tokens)-1 || tokens[end].text != "}" {
			return module, fmt.Errorf("cfg(test) module must be the final item")
		}
		return rustModule{tokens[i].start, tokens[end].end}, nil
	}
	return module, nil
}

func rustMatch(tokens []rustToken, words ...string) bool {
	if len(tokens) < len(words) {
		return false
	}
	for i, word := range words {
		if tokens[i].text != word {
			return false
		}
	}
	return true
}

func rustName(s string) bool {
	if s == "" || !rustIdentStart(s[0]) {
		return false
	}
	for i := 1; i < len(s); i++ {
		if !rustIdent(s[i]) {
			return false
		}
	}
	return true
}

type rustToken struct {
	text              string
	start, end, depth int
}

func rustTokens(source []byte) ([]rustToken, error) {
	if !utf8.Valid(source) {
		return nil, fmt.Errorf("Rust source is not UTF-8")
	}
	if bytes.HasPrefix(source, []byte("#!")) && !bytes.HasPrefix(source, []byte("#![")) {
		return nil, fmt.Errorf("Rust shebangs are unsupported")
	}
	var tokens []rustToken
	var delimiters []byte
	for i := 0; i < len(source); {
		start := i
		end, err := rustTrivia(source, i)
		if err != nil {
			return nil, err
		}
		if end != i {
			tokens = rustDocTokens(tokens, source, i, end, len(delimiters))
			i = end
			continue
		}
		end, literal, err := rustTokenEnd(source, i)
		if err != nil {
			return nil, err
		}
		i = end
		text := string(source[start:i])
		depth := len(delimiters)
		if !literal && len(text) == 1 {
			delimiters, err = rustBalance(delimiters, text[0])
			if err != nil {
				return nil, err
			}
			depth = min(depth, len(delimiters))
		}
		tokens = append(tokens, rustToken{text, start, i, depth})
	}
	if len(delimiters) != 0 {
		return nil, fmt.Errorf("unclosed Rust delimiter")
	}
	return tokens, nil
}

func rustTokenEnd(source []byte, start int) (int, bool, error) {
	if bytes.HasPrefix(source[start:], []byte("'r#")) {
		return 0, false, fmt.Errorf("raw Rust lifetimes are unsupported")
	}
	if source[start] >= utf8.RuneSelf || source[start] < ' ' && !strings.ContainsRune("\t\r\n", rune(source[start])) {
		return 0, false, fmt.Errorf("unsupported Rust token byte at %d", start)
	}
	if source[start] >= '0' && source[start] <= '9' {
		// In macro input, 1r followed by a quote is a suffixed number and
		// an ordinary string. Restarting at its r would invent a raw string.
		return rustSuffixEnd(source, start), true, nil
	}
	end, literal, err := rustLiteral(source, start)
	if err != nil {
		return 0, false, err
	}
	if literal {
		return rustSuffixEnd(source, end), true, nil
	}
	if bytes.HasPrefix(source[start:], []byte("r#")) {
		return 0, false, fmt.Errorf("raw Rust identifiers are unsupported")
	}
	i := start + 1
	if rustIdentStart(source[start]) {
		for i < len(source) && rustIdent(source[i]) {
			i++
		}
	}
	return i, false, nil
}

func rustSuffixEnd(source []byte, start int) int {
	for start < len(source) && rustIdent(source[start]) {
		start++
	}
	return start
}

// Doc comments are attributes, not trivia that may be dropped at a boundary.
func rustDocTokens(tokens []rustToken, source []byte, start, end, depth int) []rustToken {
	text := string(source[start:end])
	if strings.HasPrefix(text, "//!") || strings.HasPrefix(text, "/*!") ||
		strings.HasPrefix(text, "///") && !strings.HasPrefix(text, "////") ||
		strings.HasPrefix(text, "/**") && !strings.HasPrefix(text, "/***") && !strings.HasPrefix(text, "/**/") {
		tokens = append(tokens, rustToken{text, start, end, depth})
	}
	return tokens
}

func rustTrivia(source []byte, i int) (int, error) {
	switch {
	case strings.ContainsRune(" \t\r\n\v\f", rune(source[i])):
		return i + 1, nil
	case bytes.HasPrefix(source[i:], []byte("//")):
		if end := bytes.IndexByte(source[i:], '\n'); end >= 0 {
			return i + end + 1, nil
		}
		return len(source), nil
	case bytes.HasPrefix(source[i:], []byte("/*")):
		return rustComment(source, i)
	default:
		return i, nil
	}
}

func rustBalance(stack []byte, token byte) ([]byte, error) {
	if strings.ContainsRune("([{", rune(token)) {
		return append(stack, token), nil
	}
	if at := strings.IndexByte(")]}", token); at >= 0 {
		if len(stack) == 0 || stack[len(stack)-1] != "([{"[at] {
			return nil, fmt.Errorf("unbalanced Rust delimiter %q", token)
		}
		return stack[:len(stack)-1], nil
	}
	return stack, nil
}

func rustIdentStart(c byte) bool { return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func rustIdent(c byte) bool      { return rustIdentStart(c) || c >= '0' && c <= '9' }

func rustComment(source []byte, start int) (int, error) {
	depth := 1
	for i := start + 2; i+1 < len(source); i++ {
		switch string(source[i : i+2]) {
		case "/*":
			depth++
			i++
		case "*/":
			depth--
			i++
			if depth == 0 {
				return i + 1, nil
			}
		}
	}
	return 0, fmt.Errorf("unclosed Rust comment at byte %d", start)
}

func rustLiteral(source []byte, start int) (int, bool, error) {
	if rustCString(source[start:]) {
		return 0, true, fmt.Errorf("edition-dependent Rust C string at byte %d is unsupported", start)
	}
	i := start
	if source[i] == 'b' {
		i++
	}
	if i < len(source) && source[i] == 'r' {
		return rustRawString(source, i)
	}
	if i >= len(source) {
		return start, false, nil
	}
	if source[i] == '"' {
		return rustString(source, i)
	}
	if source[i] != '\'' || i+1 >= len(source) {
		return start, false, nil
	}
	return rustCharacter(source, start, i)
}

func rustCString(source []byte) bool {
	if bytes.HasPrefix(source, []byte("c\"")) {
		return true
	}
	if !bytes.HasPrefix(source, []byte("cr")) {
		return false
	}
	i := 2
	for i < len(source) && source[i] == '#' {
		i++
	}
	return i < len(source) && source[i] == '"'
}

func rustRawString(source []byte, start int) (int, bool, error) {
	i := start + 1
	for i < len(source) && source[i] == '#' {
		i++
	}
	if i >= len(source) || source[i] != '"' {
		return start, false, nil
	}
	close := append([]byte{'"'}, source[start+1:i]...)
	end := bytes.Index(source[i+1:], close)
	if end < 0 {
		return 0, true, fmt.Errorf("unclosed raw Rust string at byte %d", start)
	}
	return i + 1 + end + len(close), true, nil
}

func rustString(source []byte, start int) (int, bool, error) {
	for j := start + 1; j < len(source); j++ {
		if source[j] == '\\' {
			j++
		} else if source[j] == '"' {
			return j + 1, true, nil
		}
	}
	return 0, true, fmt.Errorf("unclosed Rust string at byte %d", start)
}

func rustCharacter(source []byte, start, i int) (int, bool, error) {
	j := i + 1
	if source[j] == '\\' {
		j += 2
		if j <= len(source) && source[j-1] == 'u' && j < len(source) && source[j] == '{' {
			end := bytes.IndexByte(source[j:], '}')
			if end < 0 {
				return 0, true, fmt.Errorf("unclosed Rust character escape")
			}
			j += end + 1
		} else if j <= len(source) && source[j-1] == 'x' {
			j += 2
		}
	} else {
		_, size := utf8.DecodeRune(source[j:])
		j += size
	}
	if j < len(source) && source[j] == '\'' {
		return j + 1, true, nil
	}
	if i == start && rustIdentStart(source[i+1]) {
		for j = i + 2; j < len(source) && rustIdent(source[j]); j++ {
		}
		return j, true, nil // A lifetime or label has no closing quote.
	}
	return 0, true, fmt.Errorf("unsupported Rust character token at byte %d", start)
}
