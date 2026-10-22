package guard

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/wesnel/agent-tools/internal/publictext"
)

// publicThen is what to do once a text the guard refused is rewritten.
const publicThen = "run the `gh` command again"

// ghTextFlags are the flags of gh's pull request and issue commands that
// carry text GitHub shows, with whether each names a file holding it.
var ghTextFlags = map[string]bool{"--body": false, "-b": false, "--title": false, "-t": false,
	"--body-file": true, "-F": true}

// ghTextVerbs are the pull request and issue commands that write text.
var ghTextVerbs = set("comment", "review", "create", "edit")

// apiTextFields are the fields of a `gh api` write that carry text GitHub
// shows.
var apiTextFields = set("body", "title")

// inlineBody is a body written into a GraphQL document as a string.
var inlineBody = regexp.MustCompile(`\b(?:body|title)\s*:\s*"((?:[^"\\]|\\.)*)"`)

// checkPublicGh refuses a gh call, args being the words after gh, whose
// text for GitHub names what publictext finds: a pull request's or issue's
// title, body or comment, or a `gh api` write's body or title field, its
// --input, or a body written into its GraphQL. A file is read relative to
// dir; text the guard cannot read, from stdin, the shell or a file it
// cannot open, goes unchecked here.
func checkPublicGh(args []string, expanded []bool, dir string) string {
	var texts []string
	var words []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-R" || a == "--repo" || a == "--hostname":
			i++
		case !strings.HasPrefix(a, "-"):
			words = append(words, a)
		}
	}
	switch {
	case len(words) >= 2 && (words[0] == "pr" || words[0] == "issue") && ghTextVerbs[words[1]]:
		texts = ghTexts(args, expanded, dir)
	case len(words) >= 1 && words[0] == "api":
		texts = apiTexts(args, expanded, dir)
	}
	if len(texts) == 0 {
		return ""
	}
	findings := publictext.Check(strings.Join(texts, "\n\n"), publictext.In(dir))
	if len(findings) == 0 {
		return ""
	}
	return publictext.Refusal("The text for GitHub", findings, publicThen)
}

// ghTexts are the texts a pull request or issue command's flags give.
func ghTexts(args []string, expanded []bool, dir string) []string {
	var texts []string
	for i := 0; i < len(args); i++ {
		name, value, attached := strings.Cut(args[i], "=")
		file, known := ghTextFlags[name]
		if !known {
			continue
		}
		at := i
		if !attached {
			if i+1 >= len(args) {
				break
			}
			i++
			value, at = args[i], i
		}
		if expanded[at] {
			continue
		}
		if !file {
			texts = append(texts, value)
		} else if data, err := readText(dir, value); err == nil {
			texts = append(texts, string(data))
		}
	}
	return texts
}

// apiTexts are the texts a `gh api` call's fields and input give.
func apiTexts(args []string, expanded []bool, dir string) []string {
	var texts []string
	for i := 0; i < len(args); i++ {
		flag, value, at := args[i], "", i
		switch {
		case strings.HasPrefix(flag, "--") && strings.Contains(flag, "="):
			flag, value, _ = strings.Cut(flag, "=")
		case len(flag) > 2 && (flag[:2] == "-f" || flag[:2] == "-F"):
			flag, value = flag[:2], flag[2:]
		case ghAPIValueFlags[flag] && i+1 < len(args):
			i++
			value, at = args[i], i
		}
		if expanded[at] {
			continue
		}
		switch flag {
		case "-f", "-F", "--field", "--raw-field":
			key, v, _ := strings.Cut(value, "=")
			if (flag == "-F" || flag == "--field") && strings.HasPrefix(v, "@") {
				data, err := readText(dir, v[1:])
				if err != nil {
					continue
				}
				v = string(data)
			}
			switch {
			case apiTextFields[key]:
				texts = append(texts, v)
			case key == "query":
				texts = append(texts, graphQLTexts(v)...)
			}
		case "--input":
			data, err := readText(dir, value)
			if err != nil {
				continue
			}
			var doc any
			if json.Unmarshal(data, &doc) != nil {
				texts = append(texts, string(data))
				continue
			}
			texts = append(texts, jsonTexts(doc)...)
		}
	}
	return texts
}

// graphQLTexts are the bodies and titles a GraphQL document writes as
// strings.
func graphQLTexts(query string) []string {
	var texts []string
	for _, m := range inlineBody.FindAllStringSubmatch(query, -1) {
		var s string
		if json.Unmarshal([]byte(`"`+m[1]+`"`), &s) != nil {
			s = m[1]
		}
		texts = append(texts, s)
	}
	return texts
}

// jsonTexts are the body and title strings anywhere in a request body, and
// those a GraphQL query in it writes.
func jsonTexts(doc any) []string {
	var texts []string
	switch v := doc.(type) {
	case map[string]any:
		for key, value := range v {
			s, isString := value.(string)
			switch {
			case isString && apiTextFields[key]:
				texts = append(texts, s)
			case isString && key == "query":
				texts = append(texts, graphQLTexts(s)...)
			default:
				texts = append(texts, jsonTexts(value)...)
			}
		}
	case []any:
		for _, value := range v {
			texts = append(texts, jsonTexts(value)...)
		}
	}
	return texts
}

// readText reads a regular file of at most a mebibyte, through any
// symlinks, path being relative to dir; never stdin.
func readText(dir, path string) ([]byte, error) {
	if path == "" || path == "-" {
		return nil, errors.New("stdin")
	}
	path = expandPath(path, dir)
	if !filepath.IsAbs(path) {
		return nil, errors.New("no directory to read it in")
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(real)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, errors.New("not a small regular file")
	}
	return os.ReadFile(real)
}
