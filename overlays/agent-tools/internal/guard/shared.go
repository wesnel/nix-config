package guard

import (
	"path"
	"path/filepath"
	"strings"
)

// IsCredential reports whether the absolute, clean path is a secret as the
// guard judges a command's file arguments: a .env file by its name, or a
// path under one of the credential paths by spelling, case folded where
// the file system folds it, by glob, and by what the file system would
// open. A command that sends a file elsewhere calls it to keep secrets out
// of what it sends.
func IsCredential(path string) bool {
	if envFile(path) {
		return true
	}
	for _, p := range credentialPaths() {
		if credentialUnder(path, p, false) {
			return true
		}
	}
	return false
}

// IsRepoCredential reports whether file, a path relative to the top of a
// repository as git lists it, would be a secret wherever the repository is
// checked out: a .env file (envFile), or a path ending, at any depth, in a
// credential path as that stands under the home directory, as
// .ssh/id_ed25519 and home/.aws/config do for ~/.ssh/id_ and ~/.aws/. The
// last name matches as IsCredential matches the credential path's: as a
// prefix, but a directory's (one ending in /) and a glob's whole, and with
// anything under it. A credential path outside the home directory matches
// by its last name alone. The file's own name alone is one too where it
// is a credential's name (credentialName), so that a key or login copied
// out of its directory is still one; a directory's name alone is not,
// where ~/.config/ngrok would make every tools/ngrok directory one. Names
// are case folded where the file system folds them. A command that sends
// a repository elsewhere calls it on every path its history holds.
func IsRepoCredential(file string) bool {
	if envFile(file) {
		return true
	}
	names := strings.Split(fold(file), "/")
	for _, p := range credentialPaths() {
		if endsIn(names, p) || credentialName(names[len(names)-1], p) {
			return true
		}
	}
	return false
}

// credentialName reports whether name, a file's own name, case folded, is
// what the credential path p names a file, wherever it stands. A
// directory's entry (ending in /) and a glob's name no file; otherwise p's
// last name is:
//   - a prefix ending in _, as id_ of ~/.ssh/id_: any name it starts with
//     no extension after it, so id_ed25519 but not id_ed25519.pub or a
//     source file such as id_parse.go;
//   - a name with an extension, as auth.json: that name;
//   - a bare name, of a file or a directory of configuration, as .netrc
//     or ngrok of ~/.config/ngrok: that name, or one it starts ending in
//     .yml or .yaml, as ngrok.yml and ngrok-dev.yaml, but not ngrok.go.
func credentialName(name, p string) bool {
	if strings.HasSuffix(p, "/") || strings.ContainsAny(p, "*?[") {
		return false
	}
	last := fold(path.Base(p))
	switch {
	case strings.HasSuffix(last, "_"):
		rest, ok := strings.CutPrefix(name, last)
		return ok && rest != "" && !strings.Contains(rest, ".")
	case strings.Contains(last[1:], "."):
		return name == last
	default:
		return name == last || strings.HasPrefix(name, last) && (strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml"))
	}
}

// endsIn reports whether names, a path's, hold the credential path p's
// names under the home directory, or its last name outside it, ending at
// one of names.
func endsIn(names []string, p string) bool {
	dir := strings.HasSuffix(p, "/")
	p, h := fold(strings.TrimSuffix(p, "/")), fold(home)
	var tail []string
	switch rest, under := strings.CutPrefix(p, h+"/"); {
	case p == h:
		// The home directory itself holds every path.
		return true
	case under:
		tail = strings.Split(rest, "/")
	default:
		tail = []string{path.Base(p)}
	}
	last := tail[len(tail)-1]
	whole := dir || strings.ContainsAny(p, "*?[")
	for i := 0; i+len(tail) <= len(names); i++ {
		if matchNames(tail[:len(tail)-1], names[i:]) && matchLast(last, names[i+len(tail)-1], whole) {
			return true
		}
	}
	return false
}

// matchNames reports whether the first of names match patterns, each name
// by its whole pattern.
func matchNames(patterns, names []string) bool {
	for i, pattern := range patterns {
		if ok, _ := path.Match(pattern, names[i]); !ok {
			return false
		}
	}
	return true
}

// matchLast reports whether name matches a credential path's last name,
// pattern: whole, or as a prefix of a name that is not a public key's.
func matchLast(pattern, name string, whole bool) bool {
	if whole {
		ok, _ := path.Match(pattern, name)
		return ok
	}
	return strings.HasPrefix(name, pattern) && !strings.HasSuffix(name, ".pub")
}

// envFile reports whether path names a .env file, .env or .env.NAME, which
// the guard counts as a secret wherever it is.
func envFile(path string) bool {
	b := fold(filepath.Base(path))
	return b == ".env" || strings.HasPrefix(b, ".env.")
}

// ScratchRoots are the session's temporary directories, where an agent
// writes files that are not the repository's.
func ScratchRoots() []string { return scratchRoots() }
