package main

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParallelDownload(t *testing.T) {
	// Setup an upstream repo
	handler := func(w http.ResponseWriter, r *http.Request) {
		out := fmt.Sprintf("This is a sample content for %s", r.URL.Path)

		w.Header().Add("Last-Modified", time.Now().Format(http.TimeFormat))
		w.Header().Add("Content-Length", strconv.Itoa(len(out)))

		time.Sleep(time.Second) // simulate a slow network
		w.Write([]byte(out))
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/myrepo/", handler)

	srv := &http.Server{Addr: ":0", Handler: mux}
	ln, err := net.Listen("tcp", srv.Addr)
	require.NoError(t, err)
	go srv.Serve(ln)
	defer srv.Shutdown(context.Background())

	// setup a pacoloco proxy
	testPacolocoDir, err := os.MkdirTemp(os.TempDir(), "*-pacoloco-repo")
	require.NoError(t, err)
	defer os.RemoveAll(testPacolocoDir)

	repo := &Repo{
		URL: fmt.Sprintf("http://localhost:%d/myrepo", ln.Addr().(*net.TCPAddr).Port),
	}

	config = &Config{
		CacheDir:        testPacolocoDir,
		Port:            -1,
		PurgeFilesAfter: -1,
		DownloadTimeout: 999,
		Repos:           map[string]*Repo{"up": repo},
	}

	files := []string{
		"foobar-3.3.6-7-x86_64.pkg.tar.zst",
		"bar-222.pkg.tar.zst",
		"linux-5.19.pkg.tar.zst",
		"hello-5.19.pkg.tar.zst",
		"gcc-3.pkg.tar.zst",
	}

	const num = 300
	counter := sync.WaitGroup{}
	counter.Add(num)

	for range num {
		go func() {
			defer counter.Done()

			f := files[rand.Int()%len(files)]
			content := "This is a sample content for /myrepo/" + f

			req := httptest.NewRequest(http.MethodGet, "/repo/up/"+f, nil)

			// half of requests will have a byte-range set
			if rand.Int()%2 == 0 {
				start := rand.Int() % (len(content) - 5)
				end := start + rand.Int()%(len(content)-start-1) + 1

				content = content[start : end+1]
				req.Header.Add("Range", fmt.Sprintf("bytes=%d-%d", start, end))
			}

			w := httptest.NewRecorder()
			require.NoError(t, handleRequest(w, req))
			res := w.Result()
			defer res.Body.Close()
			data, err := io.ReadAll(res.Body)
			require.NoError(t, err)
			require.Equal(t, content, string(data))
		}()
	}

	// goroutine for randomly dropping cache files

	counter.Wait()
}

func TestDownloadTimeout(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3 * time.Second) // delay longer than timeout
		w.Write([]byte("delayed response"))
	}
	mirror := httptest.NewServer(http.HandlerFunc(handler))
	defer mirror.Close()

	testDir := t.TempDir()

	config = &Config{
		CacheDir:        testDir,
		Port:            -1,
		DownloadTimeout: 1, // 1 second timeout
		Repos: map[string]*Repo{
			"timeout-repo": {URL: mirror.URL},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/repo/timeout-repo/test-1-1-any.pkg.tar.zst", nil)
	w := httptest.NewRecorder()
	err := handleRequest(w, req)
	require.Error(t, err)
}

func TestDownloadBadStatusCode(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}
	mirror := httptest.NewServer(http.HandlerFunc(handler))
	defer mirror.Close()

	testDir := t.TempDir()

	config = &Config{
		CacheDir:        testDir,
		Port:            -1,
		DownloadTimeout: 10,
		Repos: map[string]*Repo{
			"bad-repo": {URL: mirror.URL},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/repo/bad-repo/test-1-1-any.pkg.tar.zst", nil)
	w := httptest.NewRecorder()
	err := handleRequest(w, req)
	require.Error(t, err)
}

func TestDownloadNotModified(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotModified)
	}
	mirror := httptest.NewServer(http.HandlerFunc(handler))
	defer mirror.Close()

	testDir := t.TempDir()

	config = &Config{
		CacheDir:        testDir,
		Port:            -1,
		DownloadTimeout: 10,
		Repos: map[string]*Repo{
			"notmod-repo": {URL: mirror.URL},
		},
	}

	// Create a cached file so the downloader sends If-Modified-Since
	cachePath := testDir + "/pkgs/notmod-repo"
	require.NoError(t, os.MkdirAll(cachePath, os.ModePerm))
	require.NoError(t, os.WriteFile(cachePath+"/test.db", []byte("cached"), os.ModePerm))

	req := httptest.NewRequest(http.MethodGet, "/repo/notmod-repo/test.db", nil)
	w := httptest.NewRecorder()
	err := handleRequest(w, req)
	require.NoError(t, err)
	// Should serve from cache (304 means use cached version)
	require.Equal(t, http.StatusOK, w.Code)
}

// TestConcurrentForceCheckRequests is a regression test for a race in the
// Downloader lifecycle: releasing the last reference used to decide on
// cleanup outside downloadersMutex, so a client attaching to the Downloader
// at that moment could read from an already-closed buffer file, and a stale
// cleanup could delete the map entry and buffer file of a freshly created
// Downloader for the same key.
//
// Files that force an upstream check (.db) create a short-lived Downloader
// on every request even when cached, so hammering one such path from many
// goroutines exercises the attach/release window continuously.
func TestConcurrentForceCheckRequests(t *testing.T) {
	content := "pacoloco test db content"
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		_, _ = w.Write([]byte(content))
	}
	mirror := httptest.NewServer(http.HandlerFunc(handler))
	defer mirror.Close()

	config = &Config{
		CacheDir:        t.TempDir(),
		Port:            -1,
		DownloadTimeout: 10,
		Repos:           map[string]*Repo{"race-repo": {URL: mirror.URL}},
	}

	const (
		clients           = 32
		requestsPerClient = 64
	)

	errCh := make(chan error, clients)
	var wg sync.WaitGroup
	for range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range requestsPerClient {
				req := httptest.NewRequest(http.MethodGet, "/repo/race-repo/test.db", nil)
				w := httptest.NewRecorder()
				if err := handleRequest(w, req); err != nil {
					errCh <- fmt.Errorf("handleRequest: %w", err)
					return
				}
				if body := w.Body.String(); body != content {
					errCh <- fmt.Errorf("got body %q, want %q", body, content)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}
}

// TestConcurrentDownloadsShareUpstreamRequest verifies the single-flight
// behavior: concurrent clients of one uncached file must share a single
// upstream download and each receive the complete content.
func TestConcurrentDownloadsShareUpstreamRequest(t *testing.T) {
	content := strings.Repeat("pacoloco", 128*1024) // 1 MiB

	var upstreamHits atomic.Int32
	handler := func(w http.ResponseWriter, r *http.Request) {
		upstreamHits.Add(1)
		w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		// Trickle the body so every client attaches while the download
		// is still in flight.
		half := len(content) / 2
		_, _ = w.Write([]byte(content[:half]))
		w.(http.Flusher).Flush()
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte(content[half:]))
	}
	mirror := httptest.NewServer(http.HandlerFunc(handler))
	defer mirror.Close()

	config = &Config{
		CacheDir:        t.TempDir(),
		Port:            -1,
		DownloadTimeout: 10,
		Repos:           map[string]*Repo{"flight-repo": {URL: mirror.URL}},
	}

	const clients = 16

	errCh := make(chan error, clients)
	var wg sync.WaitGroup
	for range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/repo/flight-repo/pkg-1-1-any.pkg.tar.zst", nil)
			w := httptest.NewRecorder()
			if err := handleRequest(w, req); err != nil {
				errCh <- fmt.Errorf("handleRequest: %w", err)
				return
			}
			if body := w.Body.String(); body != content {
				errCh <- fmt.Errorf("got %d bytes, want %d", len(body), len(content))
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}
	require.Equal(t, int32(1), upstreamHits.Load(),
		"concurrent clients must share one upstream download")
}

func TestParseRequestURLInvalid(t *testing.T) {
	config = &Config{}
	_, err := parseRequestURL("/invalid/path")
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not match expected format")

	_, err = parseRequestURL("")
	require.Error(t, err)

	_, err = parseRequestURL("/repofoo/bar/test.db")
	require.Error(t, err)
}

func TestRequestedFile(t *testing.T) {
	config = &Config{}

	data := []struct {
		input, urlPath, key string
	}{
		{"/repo/noPath/foobar-3.3.6-7-x86_64.pkg.tar.zst", "/foobar-3.3.6-7-x86_64.pkg.tar.zst", "noPath/foobar-3.3.6-7-x86_64.pkg.tar.zst"},
		{"/repo/extended/path/bar-222.pkg.tar.zst", "/path/bar-222.pkg.tar.zst", "extended/path/bar-222.pkg.tar.zst"},
		{"/repo/upstream/extra/os/x86_64/linux-5.19.pkg.tar.zst", "/extra/os/x86_64/linux-5.19.pkg.tar.zst", "upstream/extra/os/x86_64/linux-5.19.pkg.tar.zst"},
	}

	for _, d := range data {
		f, err := parseRequestURL(d.input)
		require.NoError(t, err)
		require.Equal(t, d.urlPath, f.urlPath())
		require.Equal(t, d.key, f.key())
	}
}
