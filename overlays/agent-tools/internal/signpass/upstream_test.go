package signpass

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/env"
	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/hostpaths"
	"github.com/wesnel/agent-tools/internal/operatorkey"
)

// mirrorFixture is a mirrored repository whose pushes the gate checks, under
// ctx, which observes GitHub through up.
type mirrorFixture struct {
	ctx                                       context.Context
	dir, repo, mirror, upstream, tip, profile string
	up                                        *upstreamTransport
}

// newMirrorFixture makes the fixture, its repositories initialised with
// initArgs.
func newMirrorFixture(t *testing.T, initArgs ...string) mirrorFixture {
	t.Helper()
	dir := t.TempDir()
	ctx, global := signingConfig(t, dir)
	ctx = under(ctx, "GIT_CONFIG_GLOBAL="+global)
	repo, mirror := filepath.Join(dir, "repo"), filepath.Join(dir, "mirror.git")
	gitIn(t, dir, append(append([]string{"init", "-q", "-b", "main"}, initArgs...), repo)...)
	gitIn(t, dir, append(append([]string{"init", "-q", "--bare"}, initArgs...), mirror)...)
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "upstream")
	gitIn(t, repo, "checkout", "-q", "-b", "incoming")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "incoming")
	gitIn(t, repo, "checkout", "-q", "main")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "main history")
	gitIn(t, repo, "merge", "-q", "--no-ff", "-m", "upstream merge", "incoming")
	upstream := gitIn(t, repo, "rev-parse", "HEAD")
	gitIn(t, repo, "checkout", "-q", "-b", "topic")
	gitIn(t, repo, "-c", "gpg.format=ssh", "-c", "user.signingkey="+filepath.Join(dir, "key"), "commit", "-q", "-S", "--allow-empty", "-m", "operator change")
	tip := gitIn(t, repo, "rev-parse", "HEAD")
	common, err := filepath.EvalSymlinks(filepath.Join(repo, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(hostpaths.BakedConfigHome(env.From(ctx)), "git", "config")
	f, err := os.OpenFile(profile, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprintf(f, "\n[signpass \"mirror\"]\n commonDir = %s\n destination = %s\n github = wesnel/nix-config\n", common, mirror)
	if closeErr := f.Close(); err != nil || closeErr != nil {
		t.Fatalf("profile: %v, %v", err, closeErr)
	}
	ctx = git.At(ctx, repo)
	program, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	// A fake of GitHub's advertisement, which the test must not reach.
	up := &upstreamTransport{git: program, ca: "/unused-test-ca", advertise: func(cmd *exec.Cmd) ([]byte, error) {
		return []byte("ref: refs/heads/main\tHEAD\n" + upstream + "\tHEAD\n" + upstream + "\trefs/heads/main\n"), nil
	}}
	ctx = context.WithValue(ctx, transportKey{}, up)
	return mirrorFixture{ctx, dir, repo, mirror, upstream, tip, profile, up}
}

func TestMirrorUpstream(t *testing.T) {
	t.Parallel()
	f := newMirrorFixture(t)
	if err := gate(f.ctx, f.mirror, refPush{Branch: "topic", New: f.tip}, nil); err != nil {
		t.Fatalf("authorized mirror rejected upstream history before the operator-signed tip: %v", err)
	}
}

func TestMirrorStrictFirst(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"offline", "missing upstream"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f := newMirrorFixture(t)
			gitIn(t, f.repo, "checkout", "-q", "--orphan", "signed-root")
			gitIn(t, f.repo, "-c", "gpg.format=ssh", "-c", "user.signingkey="+filepath.Join(f.dir, "key"), "commit", "-q", "-S", "--allow-empty", "-m", "signed root")
			tip := gitIn(t, f.repo, "rev-parse", "HEAD")
			observed := 0
			f.up.advertise = func(cmd *exec.Cmd) ([]byte, error) {
				observed++
				if mode == "offline" {
					return nil, fmt.Errorf("fixture offline")
				}
				missing := strings.Repeat("a", 40)
				return []byte("ref: refs/heads/main\tHEAD\n" + missing + "\tHEAD\n" + missing + "\trefs/heads/main\n"), nil
			}
			if err := gate(f.ctx, f.mirror, refPush{Branch: "topic", New: tip}, nil); err != nil {
				t.Fatalf("fully operator-signed branch needs no upstream exception: %v", err)
			}
			if observed != 0 {
				t.Fatalf("strict signatures still requested upstream proof %d times", observed)
			}
		})
	}
}

func TestMirrorDiscovery(t *testing.T) {
	t.Parallel()
	f := newMirrorFixture(t)
	// The baked state directory belongs to this fixture, never the operator.
	root := hostpaths.BakedStateHome(env.From(f.ctx))
	gitIn(t, root, "init", "-q")
	gitIn(t, root, "config", "url.https://attacker.invalid/.insteadOf", "https://github.com/")
	advertise := f.up.advertise
	f.up.advertise = func(cmd *exec.Cmd) ([]byte, error) {
		// --get-url uses the real ls-remote URL resolution without contacting
		// either service. Only the external advertisement remains a stand-in.
		for i, arg := range cmd.Args {
			if arg == "ls-remote" {
				cmd.Args = append(cmd.Args[:i], "ls-remote", "--get-url", "https://github.com/wesnel/nix-config.git")
				break
			}
		}
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(string(out)); got != "https://github.com/wesnel/nix-config.git" {
			t.Fatalf("ancestor repository redirected upstream authority to %s", got)
		}
		return advertise(cmd)
	}
	if err := gate(f.ctx, f.mirror, refPush{Branch: "topic", New: f.tip}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestMirrorConditionalProfile(t *testing.T) {
	t.Parallel()
	f := newMirrorFixture(t)
	repo := filepath.Join(filepath.Dir(hostpaths.BakedConfigHome(env.From(f.ctx))), "git", "github.com", "wesnel", "nix-config")
	if err := os.MkdirAll(filepath.Dir(repo), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(f.repo, repo); err != nil {
		t.Fatal(err)
	}
	f.repo, f.ctx = repo, git.At(f.ctx, repo)
	gitIn(t, f.repo, "config", "--file", f.profile, "signpass.mirror.commonDir", filepath.Join(repo, ".git"))
	data, err := os.ReadFile(f.profile)
	if err != nil {
		t.Fatal(err)
	}
	prefix, mapping, ok := strings.Cut(string(data), "\n[signpass")
	if !ok {
		t.Fatal("fixture mapping missing")
	}
	include := filepath.Join(filepath.Dir(f.profile), "mirror.inc")
	if err := os.WriteFile(include, []byte("[signpass"+mapping), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.profile, []byte(prefix), 0600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, f.repo, "config", "--file", f.profile, "includeIf.gitdir:~/git/github.com/wesnel/nix-config/.path", include)
	linked := filepath.Join(f.dir, "linked")
	gitIn(t, f.repo, "worktree", "add", "-q", "-b", "linked", linked)
	other := filepath.Join(f.dir, "other")
	gitIn(t, f.dir, "init", "-q", other)
	for _, tc := range []struct{ name, dir, want string }{{"primary", f.repo, "wesnel/nix-config"}, {"linked", linked, "wesnel/nix-config"}, {"unrelated", other, ""}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := git.At(f.ctx, tc.dir)
			p, err := operatorkey.Load(ctx)
			if err != nil {
				t.Fatal(err)
			}
			got, err := p.MirrorUpstream(ctx, f.mirror)
			if err != nil || got != tc.want {
				t.Fatalf("conditional mapping = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestMirrorSHA256(t *testing.T) {
	t.Parallel()
	f := newMirrorFixture(t, "--object-format=sha256")
	if len(f.upstream) != 64 {
		t.Fatal("fixture did not use SHA256")
	}
	if got, err := mirrorUpstream(f.ctx, f.mirror); err != nil || got != f.upstream {
		t.Fatalf("SHA256 upstream proof = %s, %v", got, err)
	}
}

func TestMirrorBoundary(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"new unsigned", "foreign signer", "topic-only history", "other destination", "local mapping", "conflicting mapping", "split origins", "invalid slug", "other repository", "missing ancestor", "replacement", "shallow", "graft", "hash mismatch", "unavailable proof", "changed lease"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f := newMirrorFixture(t)
			url, tip, old := f.mirror, f.tip, ""
			observed := 0
			advertise := f.up.advertise
			f.up.advertise = func(cmd *exec.Cmd) ([]byte, error) { observed++; return advertise(cmd) }
			switch mode {
			case "new unsigned":
				gitIn(t, f.repo, "commit", "-q", "--allow-empty", "-m", "unsigned new")
				tip = gitIn(t, f.repo, "rev-parse", "HEAD")
			case "foreign signer":
				key := filepath.Join(f.dir, "foreign")
				if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key).CombinedOutput(); err != nil {
					t.Fatalf("key: %v %s", err, out)
				}
				gitIn(t, f.repo, "-c", "gpg.format=ssh", "-c", "user.signingkey="+key, "commit", "-q", "-S", "--allow-empty", "-m", "foreign")
				tip = gitIn(t, f.repo, "rev-parse", "HEAD")
			case "topic-only history":
				gitIn(t, f.repo, "commit", "-q", "--allow-empty", "-m", "topic only")
				tip = gitIn(t, f.repo, "rev-parse", "HEAD")
				f.up.advertise = func(cmd *exec.Cmd) ([]byte, error) {
					observed++
					out, err := advertise(cmd)
					return append(out, []byte(tip+"\trefs/heads/topic\n")...), err
				}
			case "other destination":
				url = filepath.Join(f.dir, "other.git")
				gitIn(t, f.dir, "init", "-q", "--bare", url)
			case "local mapping":
				data, _ := os.ReadFile(f.profile)
				prefix, mapping, _ := strings.Cut(string(data), "\n[signpass")
				if err := os.WriteFile(f.profile, []byte(prefix), 0600); err != nil {
					t.Fatal(err)
				}
				file, err := os.OpenFile(filepath.Join(f.repo, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				_, err = file.WriteString("\n[signpass" + mapping)
				file.Close()
				if err != nil {
					t.Fatal(err)
				}
			case "conflicting mapping":
				gitIn(t, f.repo, "config", "--file", f.profile, "--add", "signpass.mirror.github", "evil/other")
			case "split origins":
				include := filepath.Join(filepath.Dir(f.profile), "mirror.inc")
				gitIn(t, f.repo, "config", "--file", f.profile, "--unset", "signpass.mirror.github")
				gitIn(t, f.repo, "config", "--file", include, "signpass.mirror.github", "wesnel/nix-config")
				gitIn(t, f.repo, "config", "--file", f.profile, "include.path", include)
			case "invalid slug":
				gitIn(t, f.repo, "config", "--file", f.profile, "signpass.mirror.github", "evil/x/../../other")
			case "other repository":
				gitIn(t, f.repo, "config", "--file", f.profile, "signpass.mirror.commonDir", f.mirror)
			case "missing ancestor", "replacement", "shallow", "graft", "hash mismatch":
				gitIn(t, f.repo, "checkout", "-q", "main")
				gitIn(t, f.repo, "commit", "-q", "--allow-empty", "-m", "later upstream")
				advertised := gitIn(t, f.repo, "rev-parse", "HEAD")
				f.up.advertise = func(cmd *exec.Cmd) ([]byte, error) {
					observed++
					return []byte("ref: refs/heads/main\tHEAD\n" + advertised + "\tHEAD\n" + advertised + "\trefs/heads/main\n"), nil
				}
				if mode == "replacement" {
					gitIn(t, f.repo, "replace", "--graft", advertised)
				}
				if mode == "shallow" {
					if err := os.WriteFile(filepath.Join(f.repo, ".git", "shallow"), []byte(advertised+"\n"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "graft" {
					if err := os.WriteFile(filepath.Join(f.repo, ".git", "info", "grafts"), []byte(advertised+"\n"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				path := filepath.Join(f.repo, ".git", "objects", f.upstream[:2], f.upstream[2:])
				if mode == "hash mismatch" {
					data, err := os.ReadFile(filepath.Join(f.repo, ".git", "objects", advertised[:2], advertised[2:]))
					if err != nil {
						t.Fatal(err)
					}
					if err := os.Chmod(path, 0600); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, data, 0600); err != nil {
						t.Fatal(err)
					}
				} else if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "unavailable proof":
				f.up.advertise = func(cmd *exec.Cmd) ([]byte, error) { observed++; return nil, fmt.Errorf("fixture unavailable") }
			case "changed lease":
				old = f.upstream
			}
			err := gate(f.ctx, url, refPush{Branch: "topic", New: tip, Old: old}, nil)
			if err == nil {
				t.Fatal("unsafe mirror push accepted")
			}
			if mode == "other destination" || mode == "other repository" || mode == "local mapping" {
				if !strings.Contains(err.Error(), "has no signature in the operator's signing format") {
					t.Fatalf("unmapped destination lost its strict signature refusal: %v", err)
				}
			}
			if mode == "missing ancestor" || mode == "replacement" || mode == "shallow" || mode == "graft" || mode == "hash mismatch" {
				if !strings.Contains(err.Error(), "cannot prove complete local GitHub default history") {
					t.Fatalf("failure did not come from complete history proof: %v", err)
				}
			}
			wantObservation := mode != "other destination" && mode != "local mapping" && mode != "conflicting mapping" && mode != "split origins" && mode != "invalid slug" && mode != "other repository" && mode != "changed lease"
			if wantObservation != (observed == 1) {
				t.Fatalf("upstream proof observations = %d", observed)
			}
		})
	}
}

func TestMirrorTransport(t *testing.T) {
	t.Parallel()
	f := newMirrorFixture(t)
	linked := filepath.Join(f.dir, "linked")
	gitIn(t, f.repo, "worktree", "add", "-q", "-b", "linked", linked)
	f.ctx = git.At(f.ctx, linked)
	for _, key := range []string{"HTTPS_PROXY", "ALL_PROXY", "GIT_SSL_NO_VERIFY", "GIT_SSL_CAINFO", "SSL_CERT_FILE", "CURL_CA_BUNDLE", "GIT_ASKPASS", "GIT_EXEC_PATH"} {
		f.ctx = under(f.ctx, key+"=untrusted")
	}
	// A repository-controlled URL rewrite must not steer the observation.
	gitIn(t, f.repo, "config", "url.https://evil.invalid/.insteadOf", "https://github.com/")
	advertise := f.up.advertise
	observed := false
	f.up.advertise = func(cmd *exec.Cmd) ([]byte, error) {
		observed = true
		if cmd.Path != f.up.git || cmd.Dir == linked || cmd.Dir == f.repo {
			t.Fatal("transport escaped its isolated program or directory")
		}
		for _, entry := range cmd.Env {
			if strings.Contains(entry, "untrusted") {
				t.Fatalf("transport inherited %q", entry)
			}
		}
		for _, required := range []string{"GIT_NO_LAZY_FETCH=1", "GIT_NO_REPLACE_OBJECTS=1", "GIT_GRAFT_FILE=/dev/null", "GIT_SHALLOW_FILE=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_ALLOW_PROTOCOL=https", "GIT_TERMINAL_PROMPT=0"} {
			found := false
			for _, entry := range cmd.Env {
				if entry == required {
					found = true
				}
			}
			if !found {
				t.Fatalf("transport lacks %s", required)
			}
		}
		args := strings.Join(cmd.Args[1:], " ")
		for _, required := range []string{"credential.helper=", "http.sslVerify=true", "http.sslCAInfo=/unused-test-ca", "http.followRedirects=false", "http.proxy=", "ls-remote --symref https://github.com/wesnel/nix-config.git HEAD refs/heads/*"} {
			if !strings.Contains(args, required) {
				t.Fatalf("transport lacks %q: %s", required, args)
			}
		}
		return advertise(cmd)
	}
	// The fixture's unrelated inherited Git execution path must not affect
	// local gate commands either; the isolation assertion still sees it.
	f.ctx = under(f.ctx, "GIT_EXEC_PATH=")
	if err := gate(f.ctx, f.mirror, refPush{Branch: "topic", New: f.tip}, nil); err != nil {
		t.Fatal(err)
	}
	if !observed {
		t.Fatal("linked worktree did not obtain upstream proof")
	}
}

func TestMirrorResume(t *testing.T) {
	t.Parallel()
	for _, rewrite := range []string{"pushInsteadOf", "insteadOf"} {
		t.Run(rewrite, func(t *testing.T) {
			t.Parallel()
			f := newMirrorFixture(t)
			hub := filepath.Join(f.dir, "hub.git")
			gitIn(t, f.dir, "init", "-q", "--bare", hub)
			gitIn(t, f.repo, "remote", "add", "origin", hub)
			gitIn(t, f.repo, "push", "-q", hub, "topic")
			todo := pushes{Branches: []string{"topic"}, URLs: []urlPush{
				{Remote: "origin", URL: hub, Done: true, Refs: []refPush{{Branch: "topic", New: f.tip, Done: true}}},
				{Remote: "origin", URL: f.mirror, Refs: []refPush{{Branch: "topic", New: f.tip}}},
			}}
			advertise := f.up.advertise
			observed := 0
			f.up.advertise = func(cmd *exec.Cmd) ([]byte, error) { observed++; return nil, fmt.Errorf("fixture unavailable") }
			if _, err := pushAll(f.ctx, "topic", todo); err == nil {
				t.Fatal("resume without fresh proof accepted")
			}
			path, err := statePath(f.ctx, "push", "topic")
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var saved pushes
			if err := json.Unmarshal(data, &saved); err != nil {
				t.Fatal(err)
			}
			if !saved.URLs[0].Done || !saved.URLs[0].Refs[0].Done || saved.URLs[1].Done {
				t.Fatal("partial push progress changed")
			}
			f.up.advertise = func(cmd *exec.Cmd) ([]byte, error) { observed++; return advertise(cmd) }
			redirected := filepath.Join(f.dir, "redirected.git")
			gitIn(t, f.dir, "init", "-q", "--bare", redirected)
			global := filepath.Join(f.dir, "gitconfig")
			gitIn(t, f.repo, "config", "--file", global, "url."+redirected+"."+rewrite, f.mirror)
			if _, err := pushAll(f.ctx, "topic", saved); err == nil {
				t.Fatal("saved mirror URL was redirected after authorization")
			}
			if refs := gitIn(t, redirected, "for-each-ref", "--format=%(refname)"); refs != "" {
				t.Fatalf("redirected destination received %s", refs)
			}
			data, err = os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(data, &saved); err != nil {
				t.Fatal(err)
			}
			if !saved.URLs[0].Done || saved.URLs[1].Done {
				t.Fatal("rewrite refusal changed partial progress")
			}
			gitIn(t, f.repo, "config", "--file", global, "--unset", "url."+redirected+"."+rewrite)
			if _, err := pushAll(f.ctx, "topic", saved); err != nil {
				t.Fatal(err)
			}
			if observed != 2 {
				t.Fatalf("resume observations = %d, want 2", observed)
			}
			if got := gitIn(t, f.mirror, "rev-parse", "topic"); got != f.tip {
				t.Fatalf("mirror tip = %s", got)
			}
			if got := gitIn(t, hub, "rev-parse", "topic"); got != f.tip {
				t.Fatal("completed destination changed")
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("completed state remains: %v", err)
			}
		})
	}
}

func TestMirrorHTTPS(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"authenticated", "redirect", "wrong CA"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f := newMirrorFixture(t)
			requests := 0
			packet := func(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Header.Get("Authorization") != "" {
					t.Error("upstream observation carried credentials")
				}
				if mode == "redirect" {
					http.Redirect(w, r, "/redirected", http.StatusFound)
					return
				}
				if r.URL.Path != "/wesnel/nix-config.git/info/refs" {
					t.Errorf("unexpected request %s", r.URL)
					http.Error(w, "bad path", 400)
					return
				}
				w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
				fmt.Fprint(w, packet("# service=git-upload-pack\n")+"0000"+packet(f.upstream+" HEAD\x00symref=HEAD:refs/heads/main\n")+packet(f.upstream+" refs/heads/main\n")+"0000")
			}))
			defer server.Close()
			cert := server.Certificate()
			if mode == "wrong CA" {
				// An empty trust bundle must not fall back to system trust.
				f.up.ca = filepath.Join(f.dir, "empty-ca")
				if err := os.WriteFile(f.up.ca, nil, 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				f.up.ca = filepath.Join(f.dir, "ca.pem")
				if err := os.WriteFile(f.up.ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), 0600); err != nil {
					t.Fatal(err)
				}
			}
			// Substitute only the external service address. The real Git
			// process retains its isolated environment and transport options.
			f.up.advertise = func(cmd *exec.Cmd) ([]byte, error) {
				replaced := false
				for i, arg := range cmd.Args {
					if arg == "https://github.com/wesnel/nix-config.git" {
						cmd.Args[i] = server.URL + "/wesnel/nix-config.git"
						replaced = true
					}
				}
				if !replaced {
					t.Fatal("refusing transport outside the local TLS fixture")
				}
				return cmd.Output()
			}
			err := gate(f.ctx, f.mirror, refPush{Branch: "topic", New: f.tip}, nil)
			if mode == "authenticated" {
				if err != nil || requests != 1 {
					t.Fatalf("HTTPS proof: %v; requests %d", err, requests)
				}
			} else if err == nil {
				t.Fatal("unauthenticated proof accepted")
			}
			if mode == "redirect" && requests != 1 {
				t.Fatalf("redirect was followed: %d requests", requests)
			}
		})
	}
}

func TestDefaultAdvertisement(t *testing.T) {
	t.Parallel()
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	for _, tc := range []struct {
		name, text string
		ok         bool
	}{
		{"default", "ref: refs/heads/main\tHEAD\n" + a + "\tHEAD\n" + a + "\trefs/heads/main\n", true},
		{"detached", a + "\tHEAD\n" + a + "\trefs/heads/main\n", false},
		{"topic only", "ref: refs/heads/main\tHEAD\n" + a + "\tHEAD\n" + a + "\trefs/heads/topic\n", false},
		{"moved", "ref: refs/heads/main\tHEAD\n" + a + "\tHEAD\n" + b + "\trefs/heads/main\n", false},
		{"duplicate", "ref: refs/heads/main\tHEAD\n" + a + "\tHEAD\n" + a + "\tHEAD\n" + a + "\trefs/heads/main\n", false},
		{"tag", "ref: refs/tags/v1\tHEAD\n" + a + "\tHEAD\n" + a + "\trefs/tags/v1\n", false},
		{"malformed", "not an advertisement", false},
		{"empty branch", "ref: refs/heads/\tHEAD\n" + a + "\tHEAD\n" + a + "\trefs/heads/\n", false},
		{"invalid branch", "ref: refs/heads/a..b\tHEAD\n" + a + "\tHEAD\n" + a + "\trefs/heads/a..b\n", false},
		{"invalid oid", "ref: refs/heads/main\tHEAD\nxxx\tHEAD\nxxx\trefs/heads/main\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := defaultAdvertisement(tc.text)
			if tc.ok {
				if err != nil || got != a {
					t.Fatalf("got %q, %v", got, err)
				}
			} else if err == nil {
				t.Fatal("invalid advertisement accepted")
			}
		})
	}
}
