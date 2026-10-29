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
)

func containerFixture(t *testing.T) fixture {
	t.Helper()
	f := newFixture(t)
	_, source, _, _ := runtime.Caller(0)
	for _, name := range []string{"Dockerfile", "scripts/build_container.sh", "scripts/verify_release_bundle.sh"} {
		value, err := os.ReadFile(filepath.Join(filepath.Dir(source), "../..", name))
		if err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(f.root, name), string(value), 0o755)
	}
	write(t, filepath.Join(f.root, "migrations/baseline.sql"), "-- owned fixture only\n", 0o644)
	// Docker and compiler stubs test argument/context/guard behavior only. They
	// do not qualify a real image or actual executable metadata.
	write(t, filepath.Join(filepath.Dir(f.root), "bin/docker"), `#!/usr/bin/env bash
set -euo pipefail
[[ "$1" == buildx && "$2" == build && "$3" == --load ]]
context="${!#}"
[[ -d "$context/artifacts" && -f "$context/Dockerfile" && ! -e "$context/migrations" ]]
[[ ! -e "$context/.git" && ! -e "$context/.env" && ! -e "$context/go.mod" && ! -e "$context/README.md" ]]
[[ -f "$context/scripts/verify_release_bundle.sh" && ! -e "$context/scripts/build_releases.sh" ]]
printf '%s\n' "$@" > "$CONTAINER_TEST_CALL_LOG"
find "$context" -type f | sed "s|$context/||" | sort > "$CONTAINER_TEST_CONTEXT_LOG"
if [[ "${CONTAINER_TEST_COMMIT_DURING_DOCKER:-}" == 1 ]]; then
  printf 'owned fixture commit during container packaging\n' >> README.md
  git add README.md
  git -c user.name='Container Test' -c user.email=container-test@example.invalid -c commit.gpgsign=false commit --quiet -m 'owned fixture source advancement'
fi
`, 0o755)
	f.env = append(f.env, "CONTAINER_TEST_CALL_LOG="+filepath.Join(filepath.Dir(f.root), "docker-calls"),
		"CONTAINER_TEST_CONTEXT_LOG="+filepath.Join(filepath.Dir(f.root), "docker-context"))
	f.commit(t)
	return f
}

func digestFile(t *testing.T, path string) string {
	t.Helper()
	value, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(value))
}

func refreshFixtureChecksums(t *testing.T, directory string) {
	t.Helper()
	names := []string{"reproq-darwin-amd64", "reproq-darwin-arm64", "reproq-linux-amd64", "reproq-linux-arm64", "reproq-windows-amd64.exe", "BUILD-MANIFEST.txt"}
	var sums strings.Builder
	for _, name := range names {
		fmt.Fprintf(&sums, "%s  %s\n", digestFile(t, filepath.Join(directory, name)), name)
	}
	write(t, filepath.Join(directory, "SHA256SUMS"), sums.String(), 0o644)
}

func (f fixture) verifyBundle(t *testing.T, directory string, wantSuccess bool, manifestHash, sumsHash string, target ...string) string {
	t.Helper()
	args := append([]string{"scripts/verify_release_bundle.sh", directory, manifestHash, sumsHash}, target...)
	command := exec.Command("sh", args...)
	command.Dir = f.root
	command.Env = f.env
	output, err := command.CombinedOutput()
	if (err == nil) != wantSuccess {
		t.Fatalf("bundle verification: %v\n%s", err, output)
	}
	return string(output)
}

func TestBundleConsumerRejectsInvalidArtifacts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string)
		target []string
	}{
		{"duplicate-manifest-key", func(t *testing.T, dir string) {
			path := filepath.Join(dir, "BUILD-MANIFEST.txt")
			value, _ := os.ReadFile(path)
			write(t, path, string(value)+"version=0.0.144\n", 0o644)
			refreshFixtureChecksums(t, dir)
		}, nil},
		{"malformed-manifest", func(t *testing.T, dir string) {
			write(t, filepath.Join(dir, "BUILD-MANIFEST.txt"), "not metadata\n", 0o644)
			refreshFixtureChecksums(t, dir)
		}, nil},
		{"corrupt-binary", func(t *testing.T, dir string) {
			write(t, filepath.Join(dir, "reproq-linux-arm64"), "corrupted\n", 0o644)
		}, []string{"linux", "arm64"}},
		{"missing-binary", func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "reproq-linux-arm64")); err != nil {
				t.Fatal(err)
			}
		}, []string{"linux", "arm64"}},
		{"symlink-binary", func(t *testing.T, dir string) {
			path := filepath.Join(dir, "reproq-linux-arm64")
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("reproq-linux-amd64", path); err != nil {
				t.Fatal(err)
			}
		}, []string{"linux", "arm64"}},
		{"checksum-traversal", func(t *testing.T, dir string) {
			write(t, filepath.Join(dir, "SHA256SUMS"), strings.Repeat("0", 64)+"  ../foreign\n", 0o644)
		}, nil},
		{"duplicate-checksum", func(t *testing.T, dir string) {
			path := filepath.Join(dir, "SHA256SUMS")
			value, _ := os.ReadFile(path)
			write(t, path, string(value)+strings.Split(string(value), "\n")[0]+"\n", 0o644)
		}, nil},
		{"missing-checksum", func(t *testing.T, dir string) {
			path := filepath.Join(dir, "SHA256SUMS")
			value, _ := os.ReadFile(path)
			write(t, path, strings.Join(strings.Split(string(value), "\n")[1:], "\n"), 0o644)
		}, nil},
		{"unlisted-platform", func(*testing.T, string) {}, []string{"linux", "riscv64"}},
		{"shell-in-target", func(*testing.T, string) {}, []string{"linux", "$(touch injected)"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := containerFixture(t)
			f.build(t, true, "--unreleased", "--output", "dist/candidate")
			directory := filepath.Join(f.root, "dist/candidate")
			test.mutate(t, directory)
			f.verifyBundle(t, directory, false, digestFile(t, filepath.Join(directory, "BUILD-MANIFEST.txt")), digestFile(t, filepath.Join(directory, "SHA256SUMS")), test.target...)
		})
	}
}

func TestBundleConsumerPinsBothManifestAndChecksumSet(t *testing.T) {
	f := containerFixture(t)
	f.build(t, true, "--unreleased", "--output", "dist/candidate")
	directory := filepath.Join(f.root, "dist/candidate")
	manifest := digestFile(t, filepath.Join(directory, "BUILD-MANIFEST.txt"))
	sums := digestFile(t, filepath.Join(directory, "SHA256SUMS"))
	if result := f.verifyBundle(t, directory, true, manifest, sums, "linux", "arm64"); result != "reproq-linux-arm64\n" {
		t.Fatalf("unexpected selection: %q", result)
	}
	f.verifyBundle(t, directory, false, strings.Repeat("0", 64), sums)
	f.verifyBundle(t, directory, false, manifest, strings.Repeat("0", 64))
}

func TestContainerConsumerUsesMinimalContextWithoutCompiling(t *testing.T) {
	f := containerFixture(t)
	f.build(t, true, "--unreleased", "--output", "dist/candidate")
	write(t, filepath.Join(f.root, ".env"), "OWNED_FIXTURE_SECRET=must-not-enter-context\n", 0o600)
	f.git(t, "config", "core.excludesFile", filepath.Join(f.root, "dist/ignore"))
	write(t, filepath.Join(f.root, "dist/ignore"), ".env\n", 0o644)
	before, _ := os.ReadFile(f.log)
	command := exec.Command("bash", "scripts/build_container.sh", "--artifacts", "dist/candidate", "--image", "reproq:owned-fixture")
	command.Dir, command.Env = f.root, f.env
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("container wrapper: %v\n%s", err, output)
	}
	after, _ := os.ReadFile(f.log)
	if string(before) != string(after) {
		t.Fatal("container consumer compiled another worker")
	}
	context, err := os.ReadFile(filepath.Join(filepath.Dir(f.root), "docker-context"))
	if err != nil || strings.Contains(string(context), ".env") || strings.Contains(string(context), "go.mod") {
		t.Fatalf("unexpected context: %s, %v", context, err)
	}
}

func TestContainerConsumerRejectsWrongBinarySourceBeforeDocker(t *testing.T) {
	f := containerFixture(t)
	f.build(t, true, "--unreleased", "--output", "dist/candidate")
	f.env = append(f.env, "CONTAINER_TEST_WRONG_REVISION=1")
	command := exec.Command("bash", "scripts/build_container.sh", "--artifacts", "dist/candidate", "--image", "reproq:owned-fixture")
	command.Dir, command.Env = f.root, f.env
	if output, err := command.CombinedOutput(); err == nil || !strings.Contains(string(output), "build metadata differs") {
		t.Fatalf("wrong source accepted: %v\n%s", err, output)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(f.root), "docker-calls")); !os.IsNotExist(err) {
		t.Fatalf("invalid bundle reached Docker: %v", err)
	}
}

func TestContainerConsumerRejectsCommittedSourceAdvancement(t *testing.T) {
	for _, phase := range []string{"before", "during"} {
		t.Run(phase, func(t *testing.T) {
			f := containerFixture(t)
			f.build(t, true, "--unreleased", "--output", "dist/candidate")
			original := f.git(t, "rev-parse", "HEAD")
			selector := "CONTAINER_TEST_COMMIT_BEFORE_DOCKER=1"
			if phase == "during" {
				selector = "CONTAINER_TEST_COMMIT_DURING_DOCKER=1"
			}
			f.env = append(f.env, selector)
			command := exec.Command("bash", "scripts/build_container.sh", "--artifacts", "dist/candidate", "--image", "reproq:owned-fixture")
			command.Dir, command.Env = f.root, f.env
			output, err := command.CombinedOutput()
			if err == nil || !strings.Contains(string(output), "source revision changed during build") {
				t.Fatalf("committed source advancement accepted: %v\n%s", err, output)
			}
			if f.git(t, "rev-parse", "HEAD") == original || f.git(t, "status", "--porcelain") != "" {
				t.Fatal("fixture did not advance to another clean commit")
			}
			_, dockerError := os.Stat(filepath.Join(filepath.Dir(f.root), "docker-calls"))
			if (dockerError == nil) != (phase == "during") {
				t.Fatalf("unexpected Docker invocation for %s: %v", phase, dockerError)
			}
		})
	}
}
