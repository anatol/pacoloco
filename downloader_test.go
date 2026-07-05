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

// TestStalledDownloadAborts is a regression test for a permanent hang: with
// download_timeout unset there was no bound on a silently stalled upstream,
// so the download goroutine blocked forever, the Downloader stayed in the
// downloaders map and every future client of the same file hung in
// cond.Wait. The stall watchdog must abort such transfers: before the
// upstream headers arrive the client gets an error, after them the abort
// surfaces as a truncated body -- but in both cases the request finishes
// and the Downloader is cleaned up.
func TestStalledDownloadAborts(t *testing.T) {
	content := "stalled content that never fully arrives"

	newStallServer := func(t *testing.T, sendPrefix int) *httptest.Server {
		release := make(chan struct{})
		handler := func(w http.ResponseWriter, r *http.Request) {
			if sendPrefix > 0 {
				w.Header().Set("Content-Length", strconv.Itoa(len(content)))
				_, _ = w.Write([]byte(content[:sendPrefix]))
				w.(http.Flusher).Flush()
			}
			<-release // stall forever
		}
		mirror := httptest.NewServer(http.HandlerFunc(handler))
		t.Cleanup(mirror.Close)
		t.Cleanup(func() { close(release) }) // LIFO: runs before mirror.Close
		return mirror
	}

	oldStall := downloadStallTimeout
	downloadStallTimeout = 200 * time.Millisecond
	defer func() { downloadStallTimeout = oldStall }()

	run := func(t *testing.T, mirror *httptest.Server, fileName string) (*httptest.ResponseRecorder, error) {
		config = &Config{
			CacheDir:        t.TempDir(),
			Port:            -1,
			DownloadTimeout: 0, // the dangerous default: no total timeout
			Repos:           map[string]*Repo{"stall-repo": {URL: mirror.URL}},
		}

		w := httptest.NewRecorder()
		done := make(chan error, 1)
		go func() {
			req := httptest.NewRequest(http.MethodGet, "/repo/stall-repo/"+fileName, nil)
			done <- handleRequest(w, req)
		}()

		select {
		case err := <-done:
			return w, err
		case <-time.After(10 * time.Second):
			t.Fatal("request against a stalled upstream hung instead of aborting")
			return nil, nil
		}
	}

	requireDownloadersCleaned := func(t *testing.T) {
		require.Eventually(t, func() bool {
			downloadersMutex.Lock()
			defer downloadersMutex.Unlock()
			return len(downloaders) == 0
		}, time.Second, 10*time.Millisecond, "aborted Downloader must be removed from the map")
	}

	t.Run("stall before headers", func(t *testing.T) {
		_, err := run(t, newStallServer(t, 0), "stalled-1-1-any.pkg.tar.zst")
		require.Error(t, err, "a download stalled before headers must fail")
		requireDownloadersCleaned(t)
	})

	t.Run("stall mid-body", func(t *testing.T) {
		w, err := run(t, newStallServer(t, 8), "midbody-1-1-any.pkg.tar.zst")
		// The response headers were already relayed, so the abort cannot
		// become an HTTP error anymore; it must surface as truncation.
		require.NoError(t, err)
		require.Less(t, w.Body.Len(), len(content), "stalled body must be truncated, not complete")
		requireDownloadersCleaned(t)
	})
}

// TestStallWatchdogResetsBetweenPhases pins the per-phase semantics of the
// stall watchdog: connecting/receiving headers and receiving the first body
// chunk are separate phases, each with its own downloadStallTimeout budget.
// Without the reset after the headers arrive, a server that spends most of
// the budget before the headers and then delivers the body promptly was
// aborted even though no single phase stalled.
func TestStallWatchdogResetsBetweenPhases(t *testing.T) {
	content := "slow but steady wins the race"
	handler := func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(350 * time.Millisecond) // headers arrive late but in time
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		time.Sleep(350 * time.Millisecond) // body arrives late but in time
		_, _ = w.Write([]byte(content))
	}
	mirror := httptest.NewServer(http.HandlerFunc(handler))
	defer mirror.Close()

	oldStall := downloadStallTimeout
	downloadStallTimeout = 500 * time.Millisecond
	defer func() { downloadStallTimeout = oldStall }()

	config = &Config{
		CacheDir:        t.TempDir(),
		Port:            -1,
		DownloadTimeout: 0,
		Repos:           map[string]*Repo{"steady-repo": {URL: mirror.URL}},
	}

	req := httptest.NewRequest(http.MethodGet, "/repo/steady-repo/steady-1-1-any.pkg.tar.zst", nil)
	w := httptest.NewRecorder()
	require.NoError(t, handleRequest(w, req))
	require.Equal(t, content, w.Body.String(),
		"a download making per-phase progress must complete in full")
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
