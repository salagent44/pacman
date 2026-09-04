package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupClient starts an in-process server and points the client at an
// isolated HOME so config, manifest and install dir never touch the real one.
func setupClient(t *testing.T) (home string) {
	t.Helper()
	ts, _ := newTestServer(t)
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("PACMAN_BIN", filepath.Join(home, "bin"))
	t.Setenv("PACMAN_URL", "")
	t.Setenv("PACMAN_TOKEN", "")
	var out bytes.Buffer
	if err := runClient([]string{"login", ts.URL, testToken}, &out); err != nil {
		t.Fatalf("login: %v", err)
	}
	return home
}

func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := runClient(args, &out)
	return out.String(), err
}

func TestClientLogin(t *testing.T) {
	home := setupClient(t)
	p := filepath.Join(home, ".config", "pacman", "config")
	st, err := os.Stat(p)
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("config mode = %o, want 600", st.Mode().Perm())
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "token="+testToken) {
		t.Errorf("config content: %q", b)
	}
	if _, err := run(t, "login", "ftp://x", "tok"); err == nil {
		t.Error("login with bad scheme should fail")
	}
	// env overrides the file and a wrong token surfaces the 401
	t.Setenv("PACMAN_TOKEN", "wrong")
	if _, err := run(t, "ls"); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("ls with wrong token: %v", err)
	}
}

func TestClientNoConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("PACMAN_URL", "")
	t.Setenv("PACMAN_TOKEN", "")
	if _, err := run(t, "ls"); err == nil || !strings.Contains(err.Error(), "pacman login") {
		t.Errorf("ls without config: %v", err)
	}
	if _, err := run(t); err != errUsage {
		t.Errorf("no args: %v", err)
	}
	if _, err := run(t, "bogus"); err == nil {
		t.Error("unknown command should fail")
	}
}

func TestClientRoundTrip(t *testing.T) {
	home := setupClient(t)
	src := filepath.Join(home, "tool")
	v1 := bytes.Repeat([]byte("v1-"), 500)
	os.WriteFile(src, v1, 0o644)

	// put
	out, err := run(t, "put", src, "tool")
	if err != nil || !strings.Contains(out, "uploaded tool") {
		t.Fatalf("put: %v %q", err, out)
	}
	if _, err := run(t, "put", src, "../evil"); err == nil {
		t.Error("put with bad name should fail locally")
	}

	// ls
	out, err = run(t, "ls")
	if err != nil || !strings.Contains(out, "tool") || !strings.Contains(out, "1.5 KB") {
		t.Fatalf("ls: %v %q", err, out)
	}

	// get to a directory
	dl := filepath.Join(home, "dl")
	os.Mkdir(dl, 0o755)
	if _, err := run(t, "get", "tool", dl); err != nil {
		t.Fatalf("get: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dl, "tool")); !bytes.Equal(b, v1) {
		t.Fatal("get: content mismatch")
	}
	if _, err := run(t, "get", "missing"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("get missing: %v", err)
	}

	// install
	out, err = run(t, "install", "tool")
	if err != nil || !strings.Contains(out, "installed tool") {
		t.Fatalf("install: %v %q", err, out)
	}
	bin := filepath.Join(home, "bin", "tool")
	st, err := os.Stat(bin)
	if err != nil || st.Mode().Perm()&0o100 == 0 {
		t.Fatalf("installed file: %v mode %v", err, st.Mode())
	}
	if _, err := run(t, "install", "nope"); err == nil || !strings.Contains(err.Error(), "not on the server") {
		t.Errorf("install missing: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".local", "share", "pacman", "installed.json")); !strings.Contains(string(b), `"tool"`) {
		t.Fatalf("manifest: %q", b)
	}

	// ls shows it as installed and current
	out, _ = run(t, "ls")
	if !strings.Contains(out, bin) || strings.Contains(out, "outdated") {
		t.Errorf("ls after install: %q", out)
	}

	// update: nothing changed
	out, err = run(t, "update")
	if err != nil || !strings.Contains(out, "tool: up to date") || !strings.Contains(out, "0 updated") {
		t.Fatalf("update (no change): %v %q", err, out)
	}

	// server gets a new version -> ls flags it, update replaces it
	v2 := bytes.Repeat([]byte("v2--"), 700)
	os.WriteFile(src, v2, 0o644)
	if _, err := run(t, "put", src, "tool"); err != nil {
		t.Fatal(err)
	}
	out, _ = run(t, "ls")
	if !strings.Contains(out, "outdated") {
		t.Errorf("ls should flag outdated: %q", out)
	}
	out, err = run(t, "update")
	if err != nil || !strings.Contains(out, "updated tool") || !strings.Contains(out, "1 updated") {
		t.Fatalf("update (changed): %v %q", err, out)
	}
	if b, _ := os.ReadFile(bin); !bytes.Equal(b, v2) {
		t.Fatal("update: content mismatch")
	}
	st, _ = os.Stat(bin)
	if st.Mode().Perm()&0o100 == 0 {
		t.Error("update lost the executable bit")
	}

	// local file deleted -> update restores it
	os.Remove(bin)
	out, err = run(t, "update", "tool")
	if err != nil || !strings.Contains(out, "updated tool") {
		t.Fatalf("update (restore): %v %q", err, out)
	}
	if _, err := run(t, "update", "never-installed"); err == nil {
		t.Error("update of uninstalled name should fail")
	}

	// no temp files left in the bin dir
	des, _ := os.ReadDir(filepath.Join(home, "bin"))
	for _, de := range des {
		if strings.HasPrefix(de.Name(), ".pacman-") {
			t.Errorf("temp file left: %s", de.Name())
		}
	}

	// rm, then update skips it gracefully
	if out, err := run(t, "rm", "tool"); err != nil || !strings.Contains(out, "deleted tool") {
		t.Fatalf("rm: %v %q", err, out)
	}
	if _, err := run(t, "rm", "tool"); err == nil {
		t.Error("second rm should 404")
	}
	out, err = run(t, "update")
	if err != nil || !strings.Contains(out, "no longer on the server") {
		t.Fatalf("update after rm: %v %q", err, out)
	}
}

func TestHumanSize(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 1023: "1023 B", 1024: "1.0 KB", 1536: "1.5 KB", 20000000: "19.1 MB", 6402210: "6.1 MB"} {
		if got := humanSize(n); got != want {
			t.Errorf("humanSize(%d) = %q, want %q", n, got, want)
		}
	}
}
