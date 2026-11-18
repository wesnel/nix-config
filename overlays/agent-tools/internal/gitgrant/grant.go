// Package gitgrant passes a one-use authorization for one MCP call from
// the guard's PreToolUse hook, which Issue runs in, to the agent_ops server,
// which Consume runs in. Grants are files under the baked state home's
// agent-tools/git-authorizations, a directory both refuse unless it is
// private (0700, no symlink on the path) and which Codex's sandbox denies
// to commands: a command that could write there could forge a grant. Nix
// names the same directory in wgn.home.llm.sandbox.deniedReads.
package gitgrant

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/wesnel/agent-tools/internal/hostpaths"
)

// Record is what a grant binds. Consume compares Tool, Session and
// Arguments, the call's arguments without its authorization as Go's
// json.Marshal writes a map (sorted keys), byte for byte. Agent is
// returned for the caller's ownership check. Call, the hook's tool_use_id,
// is required but only recorded: the MCP call does not carry it. Issue
// sets Expires a minute ahead.
type Record struct {
	Tool      string          `json:"tool"`
	Session   string          `json:"session"`
	Agent     string          `json:"agent"`
	Call      string          `json:"call"`
	Arguments json.RawMessage `json:"arguments"`
	Expires   time.Time       `json:"expires"`
}

func readRecord(path string) (Record, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return Record{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return Record{}, errors.New("authorization is not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, 64*1024+1))
	if err != nil || len(data) > 64*1024 {
		return Record{}, errors.New("authorization cannot be read")
	}
	var record Record
	if err := json.Unmarshal(data, &record); err != nil {
		return Record{}, err
	}
	return record, nil
}

func authorityDir(create bool) (string, error) {
	state := hostpaths.BakedStateHome()
	if !filepath.IsAbs(state) {
		return "", errors.New("protected state directory is not configured")
	}
	real, err := filepath.EvalSymlinks(state)
	if err != nil || real != filepath.Clean(state) {
		return "", errors.New("unsupported state directory")
	}
	dir := state
	for _, part := range []string{"agent-tools", "git-authorizations"} {
		dir = filepath.Join(dir, part)
		if create {
			if err := os.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) {
				return "", err
			}
		}
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
			return "", errors.New("authorization directory is not private")
		}
	}
	return dir, nil
}

func reap(dir string, now time.Time) error {
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	entries, err := directory.ReadDir(256)
	closeErr := directory.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	for _, entry := range entries {
		name := strings.TrimSuffix(entry.Name(), ".claimed")
		decoded, err := hex.DecodeString(name)
		if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != name {
			continue
		}
		record, err := readRecord(filepath.Join(dir, entry.Name()))
		if err == nil && !record.Expires.IsZero() && !record.Expires.After(now) {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
	return nil
}

// Issue writes r as a new grant, removing expired ones first, and returns
// its token: 64 random hex digits, the grant's file name.
func Issue(r Record) (string, error) {
	if r.Tool == "" || r.Session == "" || r.Call == "" || !json.Valid(r.Arguments) {
		return "", errors.New("incomplete hook authorization")
	}
	r.Expires = time.Now().Add(time.Minute)
	data, err := json.Marshal(r)
	if err != nil || len(data) > 64*1024 {
		return "", errors.New("hook authorization is too large")
	}
	dir, err := authorityDir(true)
	if err != nil {
		return "", err
	}
	if err := reap(dir, time.Now()); err != nil {
		return "", err
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(random[:])
	f, err := os.OpenFile(filepath.Join(dir, token), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	_, err = f.Write(data)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(filepath.Join(dir, token))
		return "", err
	}
	return token, nil
}

// Consume claims the grant token names by renaming it, so one caller at
// most gets it, removes it, and returns its Record if it is unexpired and
// binds tool, session and args. Every failure is the same refusal.
func Consume(token, tool, session string, args []byte) (Record, error) {
	invalid := errors.New("invalid or expired hook authorization")
	decoded, err := hex.DecodeString(token)
	if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != token || session == "" {
		return Record{}, invalid
	}
	dir, err := authorityDir(false)
	if err != nil {
		return Record{}, invalid
	}
	source, claimed := filepath.Join(dir, token), filepath.Join(dir, token+".claimed")
	if err := os.Rename(source, claimed); err != nil {
		return Record{}, invalid
	}
	defer os.Remove(claimed)
	record, err := readRecord(claimed)
	if err != nil || record.Tool != tool || record.Session != session ||
		record.Call == "" || !record.Expires.After(time.Now()) || !bytes.Equal(record.Arguments, args) {
		return Record{}, invalid
	}
	return record, nil
}
