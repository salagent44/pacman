package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testToken = "s3cret"

func newTestServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	ts := httptest.NewServer(newServer(dir, testToken).Handler())
	t.Cleanup(ts.Close)
	return ts, dir
}

func do(t *testing.T, method, url string, body io.Reader, hdr map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func bearer() map[string]string { return map[string]string{"Authorization": "Bearer " + testToken} }

func TestAuth(t *testing.T) {
	ts, _ := newTestServer(t)
	cases := []struct {
		name string
		url  string
		hdr  map[string]string
		want int
	}{
		{"no token", ts.URL + "/files", nil, 401},
		{"wrong header token", ts.URL + "/files", map[string]string{"Authorization": "Bearer nope"}, 401},
		{"wrong query token", ts.URL + "/files?token=nope", nil, 401},
		{"header token", ts.URL + "/files", bearer(), 200},
		{"query token", ts.URL + "/files?token=" + testToken, nil, 200},
		{"healthz needs no token", ts.URL + "/healthz", nil, 200},
		{"unknown path still needs token", ts.URL + "/whatever", nil, 401},
		{"unknown path with token is 404", ts.URL + "/whatever", bearer(), 404},
	}
	for _, c := range cases {
		resp := do(t, "GET", c.url, nil, c.hdr)
		resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("%s: got %d want %d", c.name, resp.StatusCode, c.want)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	ts, dir := newTestServer(t)
	payload := bytes.Repeat([]byte("pacman\x00\xff"), 1000)

	// upload (PUT) -> 201
	resp := do(t, "PUT", ts.URL+"/files/tool.bin", bytes.NewReader(payload), bearer())
	if resp.StatusCode != 201 {
		t.Fatalf("upload: got %d", resp.StatusCode)
	}
	var e entry
	json.NewDecoder(resp.Body).Decode(&e)
	resp.Body.Close()
	if e.Name != "tool.bin" || e.Size != int64(len(payload)) || e.Path != "/files/tool.bin" || !strings.HasSuffix(e.URL, "/files/tool.bin") {
		t.Fatalf("upload entry: %+v", e)
	}

	// overwrite (POST) -> 200
	resp = do(t, "POST", ts.URL+"/files/tool.bin", bytes.NewReader(payload), bearer())
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("overwrite: got %d", resp.StatusCode)
	}

	// no scratch files left behind
	des, _ := os.ReadDir(dir)
	for _, de := range des {
		if strings.HasPrefix(de.Name(), tmpPrefix) {
			t.Errorf("temp file left behind: %s", de.Name())
		}
	}

	// list
	resp = do(t, "GET", ts.URL+"/files?token="+testToken, nil, nil)
	var lst struct{ Files []entry }
	json.NewDecoder(resp.Body).Decode(&lst)
	resp.Body.Close()
	if len(lst.Files) != 1 || lst.Files[0].Name != "tool.bin" || lst.Files[0].Size != int64(len(payload)) {
		t.Fatalf("list: %+v", lst.Files)
	}

	// download, byte-exact
	resp = do(t, "GET", ts.URL+"/files/tool.bin?token="+testToken, nil, nil)
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.Equal(got, payload) {
		t.Fatalf("download: status %d, %d bytes", resp.StatusCode, len(got))
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, `filename="tool.bin"`) {
		t.Errorf("content-disposition: %q", cd)
	}

	// range
	resp = do(t, "GET", ts.URL+"/files/tool.bin", nil, map[string]string{"Authorization": "Bearer " + testToken, "Range": "bytes=0-3"})
	got, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 206 || string(got) != "pacm" {
		t.Fatalf("range: status %d body %q", resp.StatusCode, got)
	}

	// HEAD
	resp = do(t, "HEAD", ts.URL+"/files/tool.bin", nil, bearer())
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.ContentLength != int64(len(payload)) {
		t.Fatalf("head: status %d len %d", resp.StatusCode, resp.ContentLength)
	}

	// delete
	resp = do(t, "DELETE", ts.URL+"/files/tool.bin", nil, bearer())
	resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Fatalf("delete: got %d", resp.StatusCode)
	}
	resp = do(t, "GET", ts.URL+"/files/tool.bin", nil, bearer())
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("after delete: got %d", resp.StatusCode)
	}
	resp = do(t, "DELETE", ts.URL+"/files/tool.bin", nil, bearer())
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("double delete: got %d", resp.StatusCode)
	}
}

func TestEmptyUploadRejected(t *testing.T) {
	ts, _ := newTestServer(t)
	resp := do(t, "POST", ts.URL+"/files/x", nil, bearer())
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("empty upload: got %d", resp.StatusCode)
	}
}

func TestBadNames(t *testing.T) {
	ts, dir := newTestServer(t)
	// plant a hidden file and a scratch file to prove they are invisible
	os.WriteFile(filepath.Join(dir, ".hidden"), []byte("x"), 0o600)
	os.WriteFile(filepath.Join(dir, tmpPrefix+"abc"), []byte("x"), 0o600)

	for _, n := range []string{"..", ".hidden", ".tmp-abc", "-dash", "a b", "a%2Fb", "a/b", strings.Repeat("a", 256)} {
		resp := do(t, "PUT", ts.URL+"/files/"+n, strings.NewReader("data"), bearer())
		resp.Body.Close()
		if resp.StatusCode != 400 && resp.StatusCode != 404 {
			t.Errorf("upload %q: got %d, want 400/404", n, resp.StatusCode)
		}
		resp = do(t, "GET", ts.URL+"/files/"+n, nil, bearer())
		resp.Body.Close()
		if resp.StatusCode == 200 {
			t.Errorf("download %q: unexpectedly succeeded", n)
		}
	}

	resp := do(t, "GET", ts.URL+"/files", nil, bearer())
	var lst struct{ Files []entry }
	json.NewDecoder(resp.Body).Decode(&lst)
	resp.Body.Close()
	if len(lst.Files) != 0 {
		t.Errorf("hidden/scratch files listed: %+v", lst.Files)
	}
}

func TestRedactedPath(t *testing.T) {
	r := httptest.NewRequest("GET", "/files/x?token=abc&foo=1", nil)
	got := redactedPath(r)
	if strings.Contains(got, "abc") || !strings.Contains(got, "REDACTED") {
		t.Fatalf("redactedPath: %q", got)
	}
}
