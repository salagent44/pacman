package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalURL(t *testing.T) {
	cases := map[string]string{
		":8080":          "http://127.0.0.1:8080",
		"0.0.0.0:9000":   "http://127.0.0.1:9000",
		"[::]:8080":      "http://127.0.0.1:8080",
		"127.0.0.1:8080": "http://127.0.0.1:8080",
		"10.0.0.5:81":    "http://10.0.0.5:81",
	}
	for in, want := range cases {
		got, err := localURL(in)
		if err != nil || got != want {
			t.Errorf("localURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "8080", "host", "host:"} {
		if _, err := localURL(bad); err == nil {
			t.Errorf("localURL(%q) should fail", bad)
		}
	}
}

func TestResolveInstallConfig(t *testing.T) {
	// fresh box: defaults and a generated token
	addr, tok, src, err := resolveInstallConfig(map[string]string{}, "", "")
	if err != nil || addr != defaultAddr || src != tokenGenerated || len(tok) != 64 {
		t.Fatalf("fresh: %q %q %v %v", addr, tok, src, err)
	}
	// re-run keeps what is there
	existing := map[string]string{"PACMAN_TOKEN": "keepme", "PACMAN_ADDR": "127.0.0.1:9999"}
	addr, tok, src, err = resolveInstallConfig(existing, "", "")
	if err != nil || addr != "127.0.0.1:9999" || tok != "keepme" || src != tokenFromFile {
		t.Fatalf("rerun: %q %q %v %v", addr, tok, src, err)
	}
	// explicit flags win
	addr, tok, src, err = resolveInstallConfig(existing, ":8080", "newtok")
	if err != nil || addr != ":8080" || tok != "newtok" || src != tokenFromFlag {
		t.Fatalf("explicit: %q %q %v %v", addr, tok, src, err)
	}
	if _, _, _, err := resolveInstallConfig(nil, "nonsense", ""); err == nil {
		t.Error("bad addr should fail")
	}
}

func TestEnvFileRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "env")
	m, err := readEnvFile(p)
	if err != nil || len(m) != 0 {
		t.Fatalf("missing file: %v %v", m, err)
	}
	if err := writeEnvFile(p, map[string]string{"PACMAN_TOKEN": "abc", "PACMAN_ADDR": ":8080", "EXTRA": "x=y"}); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Errorf("env mode %o", st.Mode().Perm())
	}
	m, err = readEnvFile(p)
	if err != nil || m["PACMAN_TOKEN"] != "abc" || m["PACMAN_ADDR"] != ":8080" || m["EXTRA"] != "x=y" {
		t.Fatalf("round trip: %v %v", m, err)
	}
	b, _ := os.ReadFile(p)
	if !strings.HasPrefix(string(b), "# managed by") {
		t.Errorf("header missing: %q", b)
	}
}

func TestUnitFile(t *testing.T) {
	for _, want := range []string{
		"ExecStart=" + installBin + " serve -dir " + installData,
		"EnvironmentFile=" + installEnv,
		"DynamicUser=yes",
		"StateDirectory=pacman",
		"WantedBy=multi-user.target",
	} {
		if !strings.Contains(unitFile, want) {
			t.Errorf("unit file lacks %q", want)
		}
	}
}

func TestWriteAtomic(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "a", "b", "file")
	n, err := writeAtomic(dest, strings.NewReader("hello"), 0o755)
	if err != nil || n != 5 {
		t.Fatal(n, err)
	}
	st, _ := os.Stat(dest)
	if st.Mode().Perm() != 0o755 {
		t.Errorf("mode %o", st.Mode().Perm())
	}
	if _, err := writeAtomic(dest, strings.NewReader("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dest); string(b) != "v2" {
		t.Errorf("content %q", b)
	}
	des, _ := os.ReadDir(filepath.Dir(dest))
	if len(des) != 1 {
		t.Errorf("leftover files: %v", des)
	}
}
