package guard

// CredentialPaths are the path prefixes of credentials the guard keeps out
// of every session, for a command that sends a file elsewhere to keep them
// out of what it sends.
func CredentialPaths() []string { return credentialPaths() }

// ScratchRoots are the session's temporary directories, where an agent
// writes files that are not the repository's.
func ScratchRoots() []string { return scratchRoots() }
