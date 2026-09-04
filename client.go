package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

var errUsage = errors.New("usage")

// ---- configuration -------------------------------------------------------

type clientConfig struct {
	URL   string
	Token string
}

func configPath() (string, error) {
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "pacman", "config"), nil
}

func dataDir() (string, error) {
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "pacman"), nil
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, ".local", "share", "pacman"), nil
}

func defaultBinDir() (string, error) {
	if x := os.Getenv("PACMAN_BIN"); x != "" {
		return x, nil
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, ".local", "bin"), nil
}

// loadConfig reads ~/.config/pacman/config (key=value lines) and lets
// PACMAN_URL / PACMAN_TOKEN override it.
func loadConfig() (clientConfig, error) {
	var c clientConfig
	if p, err := configPath(); err == nil {
		if b, err := os.ReadFile(p); err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				k, v, ok := strings.Cut(line, "=")
				if !ok {
					continue
				}
				switch strings.TrimSpace(k) {
				case "url":
					c.URL = strings.TrimSpace(v)
				case "token":
					c.Token = strings.TrimSpace(v)
				}
			}
		}
	}
	if v := os.Getenv("PACMAN_URL"); v != "" {
		c.URL = v
	}
	if v := os.Getenv("PACMAN_TOKEN"); v != "" {
		c.Token = v
	}
	if c.URL == "" || c.Token == "" {
		return c, errors.New("no server configured: run `pacman login URL TOKEN` or set PACMAN_URL and PACMAN_TOKEN")
	}
	c.URL = strings.TrimRight(c.URL, "/")
	return c, nil
}

func saveConfig(c clientConfig) (string, error) {
	p, err := configPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return "", err
	}
	body := fmt.Sprintf("url=%s\ntoken=%s\n", c.URL, c.Token)
	return p, os.WriteFile(p, []byte(body), 0o600)
}

// ---- manifest of installed files ----------------------------------------

type installed struct {
	Path     string    `json:"path"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

type manifest struct {
	Installed map[string]installed `json:"installed"`
}

func manifestPath() (string, error) {
	d, err := dataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "installed.json"), nil
}

func loadManifest() (*manifest, error) {
	m := &manifest{Installed: map[string]installed{}}
	p, err := manifestPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", p, err)
	}
	if m.Installed == nil {
		m.Installed = map[string]installed{}
	}
	return m, nil
}

func (m *manifest) save() error {
	p, err := manifestPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, append(b, '\n'), 0o600)
}

// ---- HTTP client ---------------------------------------------------------

type client struct {
	cfg  clientConfig
	http *http.Client
}

func newClient(cfg clientConfig) *client {
	return &client{cfg: cfg, http: &http.Client{}}
}

func (c *client) fileURL(name string) string {
	return c.cfg.URL + "/files/" + url.PathEscape(name)
}

func (c *client) request(method, u string, body io.Reader, size int64) (*http.Response, error) {
	req, err := http.NewRequest(method, u, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.ContentLength = size
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	req.Header.Set("User-Agent", "pacman/"+version)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer resp.Body.Close()
		msg := resp.Status
		var e struct {
			Error string `json:"error"`
		}
		if b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096)); len(b) > 0 {
			if json.Unmarshal(b, &e) == nil && e.Error != "" {
				msg = resp.Status + ": " + e.Error
			}
		}
		return nil, fmt.Errorf("%s %s: %s", method, u, msg)
	}
	return resp, nil
}

func (c *client) list() ([]entry, error) {
	resp, err := c.request("GET", c.cfg.URL+"/files", nil, 0)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Files []entry `json:"files"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode list: %w", err)
	}
	return out.Files, nil
}

func (c *client) put(file, name string) (entry, error) {
	f, err := os.Open(file)
	if err != nil {
		return entry{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return entry{}, err
	}
	resp, err := c.request("PUT", c.fileURL(name), f, fi.Size())
	if err != nil {
		return entry{}, err
	}
	defer resp.Body.Close()
	var e entry
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		return entry{}, fmt.Errorf("decode upload reply: %w", err)
	}
	return e, nil
}

// download fetches name into dest atomically (temp file beside dest, then
// rename), so a half-finished download never replaces a working binary.
func (c *client) download(name, dest string, mode os.FileMode) (int64, error) {
	resp, err := c.request("GET", c.fileURL(name), nil, 0)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return writeAtomic(dest, resp.Body, mode)
}

func (c *client) remove(name string) error {
	resp, err := c.request("DELETE", c.fileURL(name), nil, 0)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// ---- commands ------------------------------------------------------------

// runClient dispatches every subcommand except "serve". Output goes to out so
// tests can capture it.
func runClient(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errUsage
	}
	cmd, rest := args[0], args[1:]

	switch cmd {
	case "version":
		fmt.Fprintln(out, version)
		return nil
	case "login":
		return cmdLogin(rest, out)
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	c := newClient(cfg)

	switch cmd {
	case "ls", "list":
		return cmdLs(c, rest, out)
	case "put", "upload":
		return cmdPut(c, rest, out)
	case "get", "download":
		return cmdGet(c, rest, out)
	case "install":
		return cmdInstall(c, rest, out)
	case "update", "upgrade":
		return cmdUpdate(c, rest, out)
	case "rm", "delete":
		return cmdRm(c, rest, out)
	default:
		return fmt.Errorf("unknown command %q (try `pacman help`)", cmd)
	}
}

func cmdLogin(args []string, out io.Writer) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: pacman login URL TOKEN")
	}
	u, err := url.Parse(args[0])
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("URL must look like http://host:8080 or https://host")
	}
	if args[1] == "" {
		return fmt.Errorf("token must not be empty")
	}
	p, err := saveConfig(clientConfig{URL: strings.TrimRight(args[0], "/"), Token: args[1]})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "saved %s\n", p)
	return nil
}

func cmdLs(c *client, args []string, out io.Writer) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: pacman ls")
	}
	files, err := c.list()
	if err != nil {
		return err
	}
	m, _ := loadManifest()
	tw := tabwriter.NewWriter(out, 0, 8, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSIZE\tMODIFIED\tINSTALLED\tURL")
	for _, f := range files {
		inst := ""
		if m != nil {
			if i, ok := m.Installed[f.Name]; ok {
				inst = i.Path
				if !i.Modified.Equal(f.Modified) || i.Size != f.Size {
					inst += " (outdated)"
				}
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", f.Name, humanSize(f.Size),
			f.Modified.Local().Format("2006-01-02 15:04"), inst, f.URL)
	}
	return tw.Flush()
}

func cmdPut(c *client, args []string, out io.Writer) error {
	if len(args) < 1 || len(args) > 2 {
		return fmt.Errorf("usage: pacman put FILE [NAME]")
	}
	file := args[0]
	name := filepath.Base(file)
	if len(args) == 2 {
		name = args[1]
	}
	if !validName.MatchString(name) {
		return fmt.Errorf("invalid name %q: use letters, digits, '.', '_' or '-', not starting with '.'", name)
	}
	e, err := c.put(file, name)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "uploaded %s (%s) -> %s\n", e.Name, humanSize(e.Size), e.URL)
	return nil
}

func cmdGet(c *client, args []string, out io.Writer) error {
	if len(args) < 1 || len(args) > 2 {
		return fmt.Errorf("usage: pacman get NAME [DEST]")
	}
	name := args[0]
	dest := name
	if len(args) == 2 {
		dest = args[1]
		if st, err := os.Stat(dest); err == nil && st.IsDir() {
			dest = filepath.Join(dest, name)
		}
	}
	n, err := c.download(name, dest, 0o644)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "downloaded %s (%s) -> %s\n", name, humanSize(n), dest)
	return nil
}

func cmdInstall(c *client, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("pacman install", flag.ContinueOnError)
	fs.SetOutput(out)
	defBin, err := defaultBinDir()
	if err != nil {
		return err
	}
	dir := fs.String("dir", defBin, "directory to install into (env PACMAN_BIN)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	names := fs.Args()
	if len(names) == 0 {
		return fmt.Errorf("usage: pacman install [-dir D] NAME...")
	}

	files, err := c.list()
	if err != nil {
		return err
	}
	byName := indexByName(files)
	m, err := loadManifest()
	if err != nil {
		return err
	}
	for _, name := range names {
		e, ok := byName[name]
		if !ok {
			return fmt.Errorf("%s: not on the server (see `pacman ls`)", name)
		}
		dest := filepath.Join(*dir, name)
		n, err := c.download(name, dest, 0o755)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		m.Installed[name] = installed{Path: dest, Size: e.Size, Modified: e.Modified}
		fmt.Fprintf(out, "installed %s (%s) -> %s\n", name, humanSize(n), dest)
	}
	return m.save()
}

func cmdUpdate(c *client, args []string, out io.Writer) error {
	m, err := loadManifest()
	if err != nil {
		return err
	}
	names := args
	if len(names) == 0 {
		for n := range m.Installed {
			names = append(names, n)
		}
		sort.Strings(names)
	}
	if len(names) == 0 {
		fmt.Fprintln(out, "nothing installed yet (see `pacman install`)")
		return nil
	}

	files, err := c.list()
	if err != nil {
		return err
	}
	byName := indexByName(files)

	updated := 0
	var firstErr error
	for _, name := range names {
		local, ok := m.Installed[name]
		if !ok {
			return fmt.Errorf("%s: not installed (see `pacman install`)", name)
		}
		srv, ok := byName[name]
		if !ok {
			fmt.Fprintf(out, "%s: no longer on the server, skipping\n", name)
			continue
		}
		st, statErr := os.Stat(local.Path)
		current := statErr == nil && st.Size() == srv.Size &&
			local.Size == srv.Size && local.Modified.Equal(srv.Modified)
		if current {
			fmt.Fprintf(out, "%s: up to date\n", name)
			continue
		}
		n, err := c.download(name, local.Path, 0o755)
		if err != nil {
			fmt.Fprintf(out, "%s: %v\n", name, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		m.Installed[name] = installed{Path: local.Path, Size: srv.Size, Modified: srv.Modified}
		updated++
		fmt.Fprintf(out, "updated %s (%s) -> %s\n", name, humanSize(n), local.Path)
	}
	if err := m.save(); err != nil {
		return err
	}
	fmt.Fprintf(out, "%d updated\n", updated)
	return firstErr
}

func cmdRm(c *client, args []string, out io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: pacman rm NAME")
	}
	if err := c.remove(args[0]); err != nil {
		return err
	}
	fmt.Fprintf(out, "deleted %s\n", args[0])
	return nil
}

func indexByName(files []entry) map[string]entry {
	m := make(map[string]entry, len(files))
	for _, f := range files {
		m[f.Name] = f
	}
	return m
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
