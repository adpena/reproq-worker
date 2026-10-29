package releasebuild

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type fixture struct {
	root string
	env  []string
	log  string
}

func write(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func (f fixture) git(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = f.root
	cmd.Env = f.env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("release builder requires a POSIX bash/git host")
	}
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate release script")
	}
	script, err := os.ReadFile(filepath.Join(filepath.Dir(source), "../../scripts/build_releases.sh"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	f := fixture{root: filepath.Join(dir, "source"), log: filepath.Join(dir, "calls")}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") {
			f.env = append(f.env, entry)
		}
	}
	f.env = append(f.env, "PATH="+filepath.Join(dir, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"), "RELEASE_TEST_CALL_LOG="+f.log,
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
	write(t, filepath.Join(f.root, "scripts/build_releases.sh"), string(script), 0o755)
	toolGuard, err := os.ReadFile(filepath.Join(filepath.Dir(source), "../../scripts/check_tools.sh"))
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(f.root, "scripts/check_tools.sh"), string(toolGuard), 0o755)
	sourceGuard, err := os.ReadFile(filepath.Join(filepath.Dir(source), "../../scripts/release_source.sh"))
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(f.root, "scripts/release_source.sh"), string(sourceGuard), 0o755)
	write(t, filepath.Join(f.root, "cmd/reproq/main.go"), "package main\nconst Version = \"0.0.144\"\n", 0o644)
	write(t, filepath.Join(f.root, "go.mod"), "module release-fixture\n\ngo 1.26.10\n", 0o644)
	write(t, filepath.Join(f.root, ".gitignore"), "dist/\n", 0o644)
	write(t, filepath.Join(f.root, "README.md"), "fixture\n", 0o644)
	// The fake compiler is only for guard, cleanup, and checksum tests. Actual
	// five-platform compilation/repeated-byte qualification runs separately.
	write(t, filepath.Join(dir, "bin/go"), `#!/usr/bin/env bash
set -euo pipefail
if [[ "$1" == version && "${2:-}" == -m ]]; then
  target="$(sed -n 's/^test artifact //p' "$3")"
  revision="$(git rev-parse HEAD)"
  [[ "${CONTAINER_TEST_WRONG_REVISION:-}" != 1 ]] || revision=0000000000000000000000000000000000000000
  printf '%s: go1.26.10\n' "$3"
  printf '\tbuild\t%s\n' "vcs.revision=$revision" 'vcs.modified=false' "GOOS=${target%/*}" "GOARCH=${target#*/}" '-tags=prod' '-trimpath=true' 'CGO_ENABLED=0'
  if [[ "${CONTAINER_TEST_COMMIT_BEFORE_DOCKER:-}" == 1 && "$target" == windows/amd64 ]]; then
    printf 'owned fixture commit during metadata inspection\n' >> README.md
    git add README.md
    git -c user.name='Container Test' -c user.email=container-test@example.invalid -c commit.gpgsign=false commit --quiet -m 'owned fixture source advancement'
  fi
  exit 0
fi
if [[ "$1" == version ]]; then printf 'go version %s test/host\n' "${RELEASE_TEST_GO_VERSION:-go1.26.10}"; exit 0; fi
if [[ "$1" == env && "$2" == GOVERSION ]]; then printf '%s\n' "${RELEASE_TEST_GO_VERSION:-go1.26.10}"; exit 0; fi
[[ "$1" == build && "$CGO_ENABLED" == 0 && "$GOAMD64" == v1 && "$GOARM64" == v8.0 && "$GOTOOLCHAIN" == local && "$GOWORK" == off && "$GOENV" == off && -z "$GOFLAGS" && -z "$GOEXPERIMENT" ]]
printf '%s/%s %s\n' "$GOOS" "$GOARCH" "$*" >> "$RELEASE_TEST_CALL_LOG"
[[ "${RELEASE_TEST_FAIL_TARGET:-}" != "$GOOS/$GOARCH" ]] || exit 7
output=''
while (($#)); do
  if [[ "$1" == -o ]]; then output="$2"; shift 2; else shift; fi
done
[[ -n "$output" ]]
printf 'test artifact %s/%s\n' "$GOOS" "$GOARCH" > "$output"
if [[ "${RELEASE_TEST_MUTATE_SOURCE:-}" == 1 ]]; then printf 'changed\n' >> README.md; fi
if [[ -n "${RELEASE_TEST_HIDE_SOURCE:-}" ]]; then
  git update-index "$RELEASE_TEST_HIDE_SOURCE" -- cmd/reproq/main.go
  printf '// hidden source change\n' >> cmd/reproq/main.go
fi
if [[ "${RELEASE_TEST_MUTATE_COMPILER:-}" == 1 ]]; then printf '\n# changed compiler\n' >> "$0"; fi
`, 0o755)
	f.git(t, "init", "--quiet")
	f.commit(t)
	return f
}

func (f fixture) commit(t *testing.T) {
	t.Helper()
	f.git(t, "add", ".")
	f.git(t, "-c", "user.name=Release Builder Test", "-c", "user.email=release-test@example.invalid", "-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "owned release builder fixture")
}

func (f fixture) build(t *testing.T, wantSuccess bool, args ...string) string {
	t.Helper()
	cmd := exec.Command("bash", append([]string{"scripts/build_releases.sh"}, args...)...)
	cmd.Dir = f.root
	cmd.Env = f.env
	out, err := cmd.CombinedOutput()
	if (err == nil) != wantSuccess {
		t.Fatalf("build %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func TestCompilerMustMatchCommittedPatchBeforeBuild(t *testing.T) {
	for _, version := range []string{"go1.26.9", "go1.25.10", "go1.27.0", "go1.27.3", "devel go1.28-fake", "go1.27rc1"} {
		t.Run(version, func(t *testing.T) {
			f := newFixture(t)
			f.env = append(f.env, "RELEASE_TEST_GO_VERSION="+version)
			f.build(t, false, "--unreleased", "--output", "dist/candidate")
			if _, err := os.Stat(f.log); !os.IsNotExist(err) {
				t.Fatalf("unsupported compiler reached build: %v", err)
			}
			if _, err := os.Stat(filepath.Join(f.root, "dist/candidate")); !os.IsNotExist(err) {
				t.Fatalf("unsupported compiler created output: %v", err)
			}
		})
	}
}

func TestCompilerChangedDuringBuildRejected(t *testing.T) {
	f := newFixture(t)
	f.env = append(f.env, "RELEASE_TEST_MUTATE_COMPILER=1")
	f.build(t, false, "--unreleased", "--output", "dist/candidate")
	if _, err := os.Stat(filepath.Join(f.root, "dist/candidate")); !os.IsNotExist(err) {
		t.Fatalf("compiler mutation published output: %v", err)
	}
}

func TestCandidateArtifactsAndChecksums(t *testing.T) {
	f := newFixture(t)
	f.env = append(f.env, "GOFLAGS=-race", "GOAMD64=v3", "GOEXPERIMENT=not-a-real-experiment", "GOWORK=/does/not/exist")
	f.build(t, true, "--unreleased", "--output", "dist/candidate with spaces")
	output := filepath.Join(f.root, "dist/candidate with spaces")
	names := []string{"reproq-darwin-amd64", "reproq-darwin-arm64", "reproq-linux-amd64", "reproq-linux-arm64", "reproq-windows-amd64.exe", "BUILD-MANIFEST.txt"}
	var expectedSums strings.Builder
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(output, name))
		if err != nil {
			t.Fatal(err)
		}
		line := fmt.Sprintf("%x  %s\n", sha256.Sum256(data), name)
		actual, err := os.ReadFile(filepath.Join(output, name+".sha256"))
		if err != nil || string(actual) != line {
			t.Fatalf("sidecar %s: %q, %v", name, actual, err)
		}
		expectedSums.WriteString(line)
	}
	sums, err := os.ReadFile(filepath.Join(output, "SHA256SUMS"))
	if err != nil || string(sums) != expectedSums.String() {
		t.Fatalf("aggregate checksum manifest: %q, %v", sums, err)
	}
	manifest, err := os.ReadFile(filepath.Join(output, "BUILD-MANIFEST.txt"))
	if err != nil || !strings.Contains(string(manifest), "release_status=UNRELEASED\n") || !strings.Contains(string(manifest), "source_revision="+f.git(t, "rev-parse", "HEAD")+"\n") {
		t.Fatalf("candidate identity: %s, %v", manifest, err)
	}
	compiler, err := os.ReadFile(filepath.Join(filepath.Dir(f.root), "bin/go"))
	if err != nil || !strings.Contains(string(manifest), fmt.Sprintf("go_binary_sha256=%x\n", sha256.Sum256(compiler))) {
		t.Fatalf("compiler identity missing: %s, %v", manifest, err)
	}
	calls, err := os.ReadFile(f.log)
	if err != nil || len(strings.Split(strings.TrimSpace(string(calls)), "\n")) != 5 {
		t.Fatalf("expected exactly five builds: %q, %v", calls, err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(calls)), "\n") {
		if !strings.Contains(line, "-mod=readonly -trimpath -buildvcs=true -tags prod") {
			t.Fatalf("noncanonical build flags: %s", line)
		}
	}
	before := string(manifest)
	f.build(t, false, "--unreleased", "--output", "dist/candidate with spaces")
	after, err := os.ReadFile(filepath.Join(output, "BUILD-MANIFEST.txt"))
	if err != nil || string(after) != before {
		t.Fatal("existing output was changed")
	}
}

func TestTagMustExistMatchVersionAndIdentifyHEAD(t *testing.T) {
	for _, kind := range []string{"missing", "version", "commit", "matching-annotated"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			tag := "v0.0.144"
			switch kind {
			case "version":
				tag = "v9.9.9"
				f.git(t, "tag", tag)
			case "commit":
				f.git(t, "tag", tag)
				write(t, filepath.Join(f.root, "README.md"), "new source\n", 0o644)
				f.commit(t)
			case "matching-annotated":
				f.git(t, "-c", "user.name=Release Builder Test", "-c", "user.email=release-test@example.invalid", "tag", "--annotate", tag, "--message", "owned test tag")
			}
			f.build(t, kind == "matching-annotated", "--tag", tag, "--output", "dist/tagged")
			if kind != "matching-annotated" {
				if _, err := os.Stat(f.log); !os.IsNotExist(err) {
					t.Fatal("invalid release identity reached compiler")
				}
			}
		})
	}
}

func TestInheritedGitSelectionDoesNotChangeSource(t *testing.T) {
	f := newFixture(t)
	f.env = append(f.env, "GIT_DIR=/not/the/source/repository", "GIT_WORK_TREE=/not/the/source/tree", "GIT_INDEX_FILE=/not/the/source/index")
	f.build(t, true, "--unreleased", "--output", "dist/candidate")
}

func TestHiddenIndexFlagsRejectedBeforeAndAfterBuild(t *testing.T) {
	for _, flag := range []string{"--assume-unchanged", "--skip-worktree"} {
		for _, timing := range []string{"before", "during"} {
			t.Run(flag+"-"+timing, func(t *testing.T) {
				f := newFixture(t)
				if timing == "before" {
					f.git(t, "update-index", flag, "--", "cmd/reproq/main.go")
					write(t, filepath.Join(f.root, "cmd/reproq/main.go"), "package main\nconst Version = \"0.0.144\"\n// hidden source change\n", 0o644)
				} else {
					f.env = append(f.env, "RELEASE_TEST_HIDE_SOURCE="+flag)
				}
				f.build(t, false, "--unreleased", "--output", "dist/rejected")
				if f.git(t, "status", "--porcelain") != "" {
					t.Fatal("regression must reproduce changes hidden from Git status")
				}
				if timing == "before" {
					if _, err := os.Stat(f.log); !os.IsNotExist(err) {
						t.Fatal("hidden source reached compiler")
					}
				}
				if _, err := os.Stat(filepath.Join(f.root, "dist/rejected")); !os.IsNotExist(err) {
					t.Fatal("hidden source produced a published candidate")
				}
			})
		}
	}
}

func TestSourceBytesCheckedEvenWhenGitStatCacheLooksClean(t *testing.T) {
	f := newFixture(t)
	f.git(t, "config", "core.trustctime", "false")
	path := filepath.Join(f.root, "cmd/reproq/main.go")
	oldTime := time.Now().Add(-10 * time.Second)
	if err := os.Chtimes(path, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	f.git(t, "update-index", "--refresh")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	write(t, path, strings.Replace(string(data), "0.0.144", "0.0.145", 1), 0o644)
	if err := os.Chtimes(path, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if f.git(t, "status", "--porcelain") != "" {
		t.Fatal("regression must reproduce same-size/mtime bytes hidden by Git stat cache")
	}
	f.build(t, false, "--unreleased", "--output", "dist/rejected")
	if _, err := os.Stat(f.log); !os.IsNotExist(err) {
		t.Fatal("uncommitted source bytes reached compiler")
	}
}

func TestCompetingPublicationDirectoryIsNeverChanged(t *testing.T) {
	for _, kind := range []string{"directory", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			realMkdir, err := exec.LookPath("mkdir")
			if err != nil {
				t.Fatal(err)
			}
			foreign := t.TempDir()
			write(t, filepath.Join(foreign, "owner-sentinel"), "foreign owner\n", 0o644)
			f.env = append(f.env, "RELEASE_TEST_REAL_MKDIR="+realMkdir, "RELEASE_TEST_COMPETING_KIND="+kind, "RELEASE_TEST_FOREIGN_DIR="+foreign)
			write(t, filepath.Join(filepath.Dir(f.log), "bin/mkdir"), `#!/usr/bin/env bash
set -euo pipefail
if [[ $# == 1 && "$1" == dist/raced ]]; then
  if [[ "$RELEASE_TEST_COMPETING_KIND" == symlink ]]; then
    ln -s "$RELEASE_TEST_FOREIGN_DIR" "$1"
  else
    "$RELEASE_TEST_REAL_MKDIR" "$1"
    printf 'foreign owner\n' > "$1/owner-sentinel"
  fi
fi
exec "$RELEASE_TEST_REAL_MKDIR" "$@"
`, 0o755)
			f.build(t, false, "--unreleased", "--output", "dist/raced")
			output := filepath.Join(f.root, "dist/raced")
			entries, err := os.ReadDir(output)
			if err != nil || len(entries) != 1 || entries[0].Name() != "owner-sentinel" {
				t.Fatalf("competing output was changed or candidate nested: %v, %v", entries, err)
			}
			sentinel, err := os.ReadFile(filepath.Join(output, "owner-sentinel"))
			if err != nil || string(sentinel) != "foreign owner\n" {
				t.Fatal("foreign output content changed")
			}
			leftovers, err := filepath.Glob(filepath.Join(f.root, "dist/raced.tmp.*"))
			if err != nil || len(leftovers) != 0 {
				t.Fatalf("race retained staging directory: %v, %v", leftovers, err)
			}
		})
	}
}

func TestSourceGuardsAndFailedBuildLeaveNoCandidate(t *testing.T) {
	for _, kind := range []string{"dirty", "untracked", "duplicate-version", "compile-failure", "source-changed"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			switch kind {
			case "dirty":
				write(t, filepath.Join(f.root, "README.md"), "dirty\n", 0o644)
			case "untracked":
				write(t, filepath.Join(f.root, "unexpected.txt"), "untracked\n", 0o644)
			case "duplicate-version":
				write(t, filepath.Join(f.root, "cmd/reproq/main.go"), "const Version = \"0.0.144\"\nconst Version = \"0.0.145\"\n", 0o644)
				f.commit(t)
			case "compile-failure":
				f.env = append(f.env, "RELEASE_TEST_FAIL_TARGET=linux/amd64")
			case "source-changed":
				f.env = append(f.env, "RELEASE_TEST_MUTATE_SOURCE=1")
			}
			f.build(t, false, "--unreleased", "--output", "dist/rejected")
			if _, err := os.Stat(filepath.Join(f.root, "dist/rejected")); !os.IsNotExist(err) {
				t.Fatal("rejected build left a candidate")
			}
			leftovers, err := filepath.Glob(filepath.Join(f.root, "dist/rejected.tmp.*"))
			if err != nil || len(leftovers) != 0 {
				t.Fatalf("failed build retained temporary artifacts: %v, %v", leftovers, err)
			}
		})
	}
}
