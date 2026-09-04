package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// validName is the single rule for what a stored object may be called. The
// leading alphanumeric excludes ".", "..", hidden files and our ".tmp-*"
// upload scratch files, so none of them can ever be addressed or listed.
var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,254}$`)

const tmpPrefix = ".tmp-"

type server struct {
	dir   string
	token []byte
}

func newServer(dir, token string) *server {
	return &server{dir: dir, token: []byte(token)}
}

// entry is one stored file as reported by the list and upload endpoints.
type entry struct {
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	Path     string    `json:"path"`
	URL      string    `json:"url"`
}

func (s *server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, "ok\n")
	})
	mux.Handle("GET /files", s.auth(s.list))
	mux.Handle("GET /files/{$}", s.auth(s.list))
	mux.Handle("PUT /files/{name}", s.auth(s.upload))
	mux.Handle("POST /files/{name}", s.auth(s.upload))
	mux.Handle("GET /files/{name}", s.auth(s.download))
	mux.Handle("DELETE /files/{name}", s.auth(s.remove))
	mux.Handle("/", s.auth(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	}))
	return logRequests(mux)
}

// auth accepts the token either as "Authorization: Bearer <t>" or as a
// "?token=<t>" query parameter, compared in constant time.
func (s *server) auth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got string
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			got = strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
		} else {
			got = r.URL.Query().Get("token")
		}
		if got == "" || subtle.ConstantTimeCompare([]byte(got), s.token) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="pacman"`)
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next(w, r)
	})
}

// name validates the {name} path value and returns it with its on-disk path.
func (s *server) name(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	n := r.PathValue("name")
	if !validName.MatchString(n) {
		writeError(w, http.StatusBadRequest, "invalid name: use letters, digits, '.', '_' or '-', not starting with '.'")
		return "", "", false
	}
	return n, filepath.Join(s.dir, filepath.Base(n)), true
}

func (s *server) entryFor(r *http.Request, name string, fi os.FileInfo) entry {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	p := "/files/" + name
	return entry{
		Name:     name,
		Size:     fi.Size(),
		Modified: fi.ModTime().UTC(),
		Path:     p,
		URL:      scheme + "://" + r.Host + p,
	}
}

func (s *server) list(w http.ResponseWriter, r *http.Request) {
	des, err := os.ReadDir(s.dir)
	if err != nil {
		log.Printf("list: %v", err)
		writeError(w, http.StatusInternalServerError, "cannot read storage")
		return
	}
	files := make([]entry, 0, len(des))
	for _, de := range des {
		if !de.Type().IsRegular() || !validName.MatchString(de.Name()) {
			continue
		}
		fi, err := de.Info()
		if err != nil {
			continue
		}
		files = append(files, s.entryFor(r, de.Name(), fi))
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}

func (s *server) upload(w http.ResponseWriter, r *http.Request) {
	name, dst, ok := s.name(w, r)
	if !ok {
		return
	}
	if r.ContentLength == 0 {
		writeError(w, http.StatusBadRequest, "empty body: send the file bytes as the request body")
		return
	}

	tmp, err := os.CreateTemp(s.dir, tmpPrefix+"*")
	if err != nil {
		log.Printf("upload %s: create temp: %v", name, err)
		writeError(w, http.StatusInternalServerError, "cannot write to storage")
		return
	}
	tmpName := tmp.Name()
	cleanup := func() { tmp.Close(); os.Remove(tmpName) }

	n, err := io.Copy(tmp, r.Body)
	if err != nil {
		cleanup()
		log.Printf("upload %s: copy body: %v", name, err)
		writeError(w, http.StatusInternalServerError, "write failed")
		return
	}
	if n == 0 {
		cleanup()
		writeError(w, http.StatusBadRequest, "empty body: send the file bytes as the request body")
		return
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		log.Printf("upload %s: sync: %v", name, err)
		writeError(w, http.StatusInternalServerError, "write failed")
		return
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		log.Printf("upload %s: close: %v", name, err)
		writeError(w, http.StatusInternalServerError, "write failed")
		return
	}
	if err := os.Chmod(tmpName, 0o640); err != nil {
		os.Remove(tmpName)
		log.Printf("upload %s: chmod: %v", name, err)
		writeError(w, http.StatusInternalServerError, "write failed")
		return
	}

	status := http.StatusCreated
	if _, err := os.Stat(dst); err == nil {
		status = http.StatusOK // overwrite
	}
	if err := os.Rename(tmpName, dst); err != nil {
		os.Remove(tmpName)
		log.Printf("upload %s: rename: %v", name, err)
		writeError(w, http.StatusInternalServerError, "write failed")
		return
	}
	fi, err := os.Stat(dst)
	if err != nil {
		log.Printf("upload %s: stat: %v", name, err)
		writeError(w, http.StatusInternalServerError, "write failed")
		return
	}
	writeJSON(w, status, s.entryFor(r, name, fi))
}

func (s *server) download(w http.ResponseWriter, r *http.Request) {
	name, p, ok := s.name(w, r)
	if !ok {
		return
	}
	f, err := os.Open(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeError(w, http.StatusNotFound, "no such file")
			return
		}
		log.Printf("download %s: open: %v", name, err)
		writeError(w, http.StatusInternalServerError, "cannot read file")
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, "no such file")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// ServeContent gives us Range (resumable downloads), HEAD and Last-Modified.
	http.ServeContent(w, r, name, fi.ModTime(), f)
}

func (s *server) remove(w http.ResponseWriter, r *http.Request) {
	name, p, ok := s.name(w, r)
	if !ok {
		return
	}
	if err := os.Remove(p); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeError(w, http.StatusNotFound, "no such file")
			return
		}
		log.Printf("delete %s: %v", name, err)
		writeError(w, http.StatusInternalServerError, "delete failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// statusWriter records the status and byte count for the request log.
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (sw *statusWriter) WriteHeader(code int) {
	if sw.status == 0 {
		sw.status = code
	}
	sw.ResponseWriter.WriteHeader(code)
}

func (sw *statusWriter) Write(b []byte) (int, error) {
	if sw.status == 0 {
		sw.status = http.StatusOK
	}
	n, err := sw.ResponseWriter.Write(b)
	sw.bytes += int64(n)
	return n, err
}

// logRequests writes one line per request. The token query parameter is
// redacted so the secret never lands in the log.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		if sw.status == 0 {
			sw.status = http.StatusOK
		}
		log.Printf("%s %s %d %dB %s %s", r.Method, redactedPath(r), sw.status, sw.bytes,
			time.Since(start).Round(time.Millisecond), r.RemoteAddr)
	})
}

func redactedPath(r *http.Request) string {
	if r.URL.RawQuery == "" {
		return r.URL.Path
	}
	q := r.URL.Query()
	if q.Has("token") {
		q.Set("token", "REDACTED")
	}
	return r.URL.Path + "?" + q.Encode()
}
