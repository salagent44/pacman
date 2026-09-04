package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// `pacman serve -install` turns the box it runs on into a pacman server:
// binary in /usr/local/bin, token in /etc/pacman/env, data in /var/lib/pacman,
// a systemd unit that survives reboots, and the binary uploaded to the drop
// so other machines can bootstrap from it. Re-running it upgrades in place
// and keeps the existing token.

const (
	installBin  = "/usr/local/bin/pacman"
	installEnv  = "/etc/pacman/env"
	installUnit = "/etc/systemd/system/pacman.service"
	installData = "/var/lib/pacman"
	unitName    = "pacman.service"
	defaultAddr = ":8080"
)

const unitFile = `[Unit]
Description=pacman binary drop
Documentation=https://github.com/salagent44/pacman
After=network-online.target
Wants=network-online.target

[Service]
# PACMAN_TOKEN and PACMAN_ADDR live here; edit and 'systemctl restart pacman'.
EnvironmentFile=` + installEnv + `
ExecStart=` + installBin + ` serve -dir ` + installData + `
DynamicUser=yes
StateDirectory=pacman
Restart=on-failure
RestartSec=2
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes

[Install]
WantedBy=multi-user.target
`

// installServer performs the installation. addr and token are the values
// given on the command line ("" when not given); existing settings in the
// env file win over defaults so a re-run never rotates the token or moves
// the listen address by accident.
func installServer(addr, token string, out io.Writer) error {
	if os.Geteuid() != 0 {
		return errors.New("install needs root: sudo pacman serve -install")
	}
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return errors.New("install needs systemd (no /run/systemd/system)")
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		return errors.New("install needs systemctl on PATH")
	}

	// 1. binary
	src, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(src); err == nil {
		src = resolved
	}
	if src == installBin {
		fmt.Fprintf(out, "binary    %s (already in place)\n", installBin)
	} else {
		f, err := os.Open(src)
		if err != nil {
			return err
		}
		_, err = writeAtomic(installBin, f, 0o755)
		f.Close()
		if err != nil {
			return fmt.Errorf("install binary: %w", err)
		}
		fmt.Fprintf(out, "binary    %s\n", installBin)
	}

	// 2. env file: token and listen address
	existing, err := readEnvFile(installEnv)
	if err != nil {
		return err
	}
	addr, token, tokenSrc, err := resolveInstallConfig(existing, addr, token)
	if err != nil {
		return err
	}
	existing["PACMAN_TOKEN"] = token
	existing["PACMAN_ADDR"] = addr
	if err := writeEnvFile(installEnv, existing); err != nil {
		return fmt.Errorf("write %s: %w", installEnv, err)
	}
	fmt.Fprintf(out, "config    %s (token, listen address)\n", installEnv)

	// 3. unit
	if err := os.WriteFile(installUnit, []byte(unitFile), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", installUnit, err)
	}
	fmt.Fprintf(out, "unit      %s\n", installUnit)
	fmt.Fprintf(out, "data      %s\n", installData)

	// 4. enable and (re)start
	for _, args := range [][]string{
		{"daemon-reload"},
		{"enable", unitName},
		{"restart", unitName},
	} {
		if b, err := exec.Command("systemctl", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(b)))
		}
	}

	// 5. wait until it answers
	base, err := localURL(addr)
	if err != nil {
		return err
	}
	if err := waitHealthy(base, 10*time.Second); err != nil {
		return fmt.Errorf("%w\n  check: journalctl -u %s -n 30", err, unitName)
	}
	fmt.Fprintf(out, "service   %s enabled and running on %s\n", unitName, addr)

	// 6. put the binary in the drop so other machines can bootstrap from it
	c := newClient(clientConfig{URL: base, Token: token})
	if _, err := c.put(installBin, "pacman"); err != nil {
		return fmt.Errorf("upload pacman to the drop: %w", err)
	}
	fmt.Fprintf(out, "uploaded  pacman to the drop (%s/files/pacman)\n", base)

	// 7. summary
	fmt.Fprintln(out)
	switch tokenSrc {
	case tokenGenerated:
		fmt.Fprintf(out, "TOKEN (shown once, save it in your password manager now):\n\n  %s\n\n", token)
	case tokenFromFlag:
		fmt.Fprintf(out, "token set from -token (in %s)\n\n", installEnv)
	default:
		fmt.Fprintf(out, "token unchanged (in %s)\n\n", installEnv)
	}
	host, _ := os.Hostname()
	if host == "" {
		host = "<host>"
	}
	port := addr[strings.LastIndex(addr, ":")+1:]
	fmt.Fprintf(out, `From any machine:
  curl -OJ 'http://%[1]s:%[2]s/files/pacman?token=<token>' && chmod +x pacman
  ./pacman login http://%[1]s:%[2]s <token>
  ./pacman install pacman

Logs:     journalctl -u %[3]s -f
Upgrade:  copy a new binary here and run 'sudo ./pacman serve -install' again
Before exposing this to the internet put TLS in front, e.g. Caddy:
  your.domain { reverse_proxy 127.0.0.1:%[2]s }
then set PACMAN_ADDR=127.0.0.1:%[2]s in %[4]s and 'systemctl restart %[3]s'.
`, host, port, unitName, installEnv)
	return nil
}

// tokenSource says where the token used by an install came from.
type tokenSource int

const (
	tokenFromFlag tokenSource = iota
	tokenFromFile
	tokenGenerated
)

// resolveInstallConfig decides the listen address and token: explicit flag,
// else what the env file already has, else the default / a fresh token.
func resolveInstallConfig(existing map[string]string, addr, token string) (string, string, tokenSource, error) {
	if addr == "" {
		addr = existing["PACMAN_ADDR"]
	}
	if addr == "" {
		addr = defaultAddr
	}
	if _, err := localURL(addr); err != nil {
		return "", "", 0, err
	}
	src := tokenFromFlag
	if token == "" {
		token = existing["PACMAN_TOKEN"]
		src = tokenFromFile
	}
	if token == "" {
		var b [32]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", "", 0, fmt.Errorf("generate token: %w", err)
		}
		token = hex.EncodeToString(b[:])
		src = tokenGenerated
	}
	return addr, token, src, nil
}

// localURL turns a listen address into a URL this host can reach it on.
func localURL(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("listen address %q must be host:port or :port", addr)
	}
	if port == "" {
		return "", fmt.Errorf("listen address %q has no port", addr)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port), nil
}

func waitHealthy(base string, timeout time.Duration) error {
	hc := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		resp, err := hc.Get(base + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			err = fmt.Errorf("healthz returned %s", resp.Status)
		}
		last = err
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("service did not come up on %s within %s: %v", base, timeout, last)
}

func readEnvFile(path string) (map[string]string, error) {
	m := map[string]string{}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			m[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return m, nil
}

func writeEnvFile(path string, m map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	sb.WriteString("# managed by 'pacman serve -install'; edit and 'systemctl restart pacman'\n")
	for _, k := range keys {
		fmt.Fprintf(&sb, "%s=%s\n", k, m[k])
	}
	_, err := writeAtomic(path, strings.NewReader(sb.String()), 0o600)
	return err
}

// writeAtomic streams r into a temp file beside dest and renames it into
// place, so a reader never sees a partial file and a running executable is
// replaced safely.
func writeAtomic(dest string, r io.Reader, mode os.FileMode) (int64, error) {
	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	tmp, err := os.CreateTemp(dir, ".pacman-*")
	if err != nil {
		return 0, err
	}
	tmpName := tmp.Name()
	fail := func(err error) (int64, error) {
		tmp.Close()
		os.Remove(tmpName)
		return 0, err
	}
	n, err := io.Copy(tmp, r)
	if err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return 0, err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		os.Remove(tmpName)
		return 0, err
	}
	if err := os.Rename(tmpName, dest); err != nil {
		os.Remove(tmpName)
		return 0, err
	}
	return n, nil
}
