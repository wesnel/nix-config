set -euo pipefail
test_root=$(mktemp -d)
export HOME="$test_root/home" GIT_CONFIG_SYSTEM=/dev/null
export GIT_CONFIG_GLOBAL="$test_root/global"
mkdir -p "$HOME/git/github.com/wesnel"
while IFS=$'\t' read -r condition path; do
  [[ -f "$path" ]] || { echo "generated include is missing: $path" >&2; exit 1; }
  git config --file "$GIT_CONFIG_GLOBAL" "includeIf.$condition.path" "$path"
done < <(jq -r '.[] | [.condition, .path] | @tsv' "$includesPath")
git config --file "$GIT_CONFIG_GLOBAL" credential.https://github.com.helper "$helper"
for repo in nix-config emacs-config; do
  dir="$HOME/git/github.com/wesnel/$repo"
  git init -q "$dir"
  git -C "$dir" config remote.origin.url "git@github.com:wesnel/$repo.git"
  actual=$(git -C "$dir" remote get-url origin)
  expected="https://github.com/wesnel/$repo.git"
  if [[ "$actual" != "$expected" ]]; then
    echo "$repo fetch: expected $expected; got $actual" >&2
    exit 1
  fi
  actual=$(git -C "$dir" remote get-url --push --all origin)
  expected=$(printf 'ssh://git@github.com/wesnel/%s.git\ngit@git.sr.ht:~wgn/%s' "$repo" "$repo")
  [[ "$actual" == "$expected" ]] || { echo "$repo push destinations differ: $actual" >&2; exit 1; }
  actual=$(git -C "$dir" config --get credential.https://github.com.helper) || {
    echo "$repo GitHub credential helper reset is missing" >&2
    exit 1
  }
  [[ -z "$actual" ]] || { echo "$repo GitHub credential helper reset is not empty" >&2; exit 1; }
done
dir="$HOME/git/github.com/wesnel/unrelated"
git init -q "$dir"
git -C "$dir" config remote.origin.url git@github.com:wesnel/unrelated.git
[[ "$(git -C "$dir" remote get-url origin)" == git@github.com:wesnel/unrelated.git ]] || {
  echo 'unrelated repository fetch URL was rewritten' >&2
  exit 1
}
echo 'fetch and push URL checks passed'
