package releasebuild

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWorkerPromotionPreservesLegacyLatest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("release promotion requires a POSIX host")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("release promotion requires jq")
	}
	cmpPath, err := exec.LookPath("cmp")
	if err != nil {
		t.Fatal(err)
	}
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate release promotion script")
	}
	script := filepath.Join(filepath.Dir(source), "../../scripts/publish_release.sh")
	revision := strings.Repeat("a", 40)
	tree := strings.Repeat("b", 40)
	manifest := fmt.Sprintf("version=0.0.146\nsource_revision=%s\nsource_tree=%s\nrelease_status=TAGGED_DRAFT_CANDIDATE\ntag=v0.0.146\nplatforms=linux/amd64\ncompiler=go version go1.26.10 linux/amd64\n", revision, tree)
	contents := map[string]string{"BUILD-MANIFEST.txt": manifest, "reproq-linux-amd64": "worker binary"}
	var checksums string
	for _, name := range []string{"BUILD-MANIFEST.txt", "reproq-linux-amd64"} {
		line := fmt.Sprintf("%x  %s\n", sha256.Sum256([]byte(contents[name])), name)
		checksums += line
		contents[name+".sha256"] = line
	}
	contents["SHA256SUMS"] = checksums

	for _, tc := range []struct {
		mode, wantError string
		wantPatch       bool
	}{
		{mode: "success", wantPatch: true},
		{mode: "remote-tag", wantError: "Remote tag"},
		{mode: "sidecar", wantError: "Sidecar differs"},
		{mode: "extra-file", wantError: "Unexpected bundle entry"},
		{mode: "asset-digest", wantError: "Draft assets differ"},
		{mode: "changed-after-verification", wantError: "Draft assets differ"},
		{mode: "latest-drift", wantError: "Restore the legacy"},
		{mode: "already-public", wantError: "Expected one unpublished stable draft"},
		{mode: "uncertain-patch", wantError: "Publication response is uncertain", wantPatch: true},
		{mode: "failed-readback", wantError: "Publication readback failed", wantPatch: true},
		{mode: "latest-drift-after-patch", wantError: "latest pointer changed", wantPatch: true},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			root := t.TempDir()
			bundle := filepath.Join(root, "bundle")
			var assets []map[string]any
			for name, data := range contents {
				write(t, filepath.Join(bundle, name), data, 0o644)
				digest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(data)))
				if tc.mode == "asset-digest" && name == "reproq-linux-amd64" {
					digest = ""
				}
				if tc.mode == "changed-after-verification" && name == "reproq-linux-amd64" {
					digest = fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("worker forged")))
				}
				assets = append(assets, map[string]any{"name": name, "digest": digest, "size": len(data), "id": name, "state": "uploaded"})
			}
			if tc.mode == "sidecar" {
				write(t, filepath.Join(bundle, "reproq-linux-amd64.sha256"), "incorrect checksum", 0o644)
			}
			if tc.mode == "extra-file" {
				write(t, filepath.Join(bundle, ".extra"), "unexpected", 0o644)
			}
			if tc.mode == "changed-after-verification" {
				write(t, filepath.Join(root, "cmp"), `#!/bin/sh
printf %s 'worker forged' > "$FIXTURE/bundle/reproq-linux-amd64"
exec "$REAL_CMP" "$@"
`, 0o755)
			}
			for _, draft := range []bool{false, true} {
				data, err := json.Marshal(map[string]any{"databaseId": 123, "tagName": "v0.0.146", "isDraft": draft, "isPrerelease": false, "assets": assets})
				if err != nil {
					t.Fatal(err)
				}
				write(t, filepath.Join(root, fmt.Sprintf("draft-%t.json", draft)), string(data), 0o644)
			}
			write(t, filepath.Join(root, "commit.json"), fmt.Sprintf(`{"sha":%q,"commit":{"tree":{"sha":%q}}}`, revision, tree), 0o644)
			write(t, filepath.Join(root, "gh"), `#!/bin/sh
set -eu
case "$1" in
  release)
    if [ "$MODE" = failed-readback ] && [ -e "$FIXTURE/patch" ]; then exit 92; fi
    if [ -e "$FIXTURE/patch" ] || [ "$MODE" = already-public ]; then cat "$FIXTURE/draft-false.json"
    else cat "$FIXTURE/draft-true.json"; fi ;;
  api)
    case "$4" in
      repos/adpena/reproq-worker/commits/refs/tags/v0.0.146)
        if [ "$MODE" = remote-tag ]; then echo '{}'; else cat "$FIXTURE/commit.json"; fi ;;
      repos/adpena/reproq-worker/releases/latest)
        if [ "$MODE" = latest-drift ] || { [ "$MODE" = latest-drift-after-patch ] && [ -e "$FIXTURE/patch" ]; }; then
          echo v0.0.146
        else
          echo v0.0.144
        fi ;;
      repos/adpena/reproq-worker/releases/123)
        printf '%s\n' "$*" >> "$FIXTURE/patch"
        [ "$MODE" != uncertain-patch ] ;;
      *) exit 90 ;;
    esac ;;
  *) exit 91 ;;
esac
`, 0o755)
			cmd := exec.Command("sh", script, "adpena/reproq-worker", "v0.0.146", bundle,
				fmt.Sprintf("%x", sha256.Sum256([]byte(manifest))), fmt.Sprintf("%x", sha256.Sum256([]byte(checksums))))
			cmd.Env = append(os.Environ(), "PATH="+root+string(os.PathListSeparator)+os.Getenv("PATH"), "FIXTURE="+root, "MODE="+tc.mode, "REAL_CMP="+cmpPath)
			out, err := cmd.CombinedOutput()
			if (err != nil) != (tc.wantError != "") || !strings.Contains(string(out), tc.wantError) {
				t.Fatalf("promotion: %v\n%s", err, out)
			}
			patch, err := os.ReadFile(filepath.Join(root, "patch"))
			if tc.wantPatch {
				const expected = "api --hostname github.com repos/adpena/reproq-worker/releases/123 --method PATCH -F draft=false -f make_latest=false\n"
				if err != nil || string(patch) != expected {
					t.Fatalf("expected exactly one explicit non-latest promotion: %v\n%s", err, patch)
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unexpected promotion attempt: %v\n%s", err, patch)
			}
		})
	}
}
