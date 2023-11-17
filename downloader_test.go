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
	"sync"
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

	for i := 0; i < num; i++ {
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
