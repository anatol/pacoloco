package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"gorm.io/gorm"
)

var configFile = flag.String("config", "/etc/pacoloco.yaml", "Path to config file")

var (
	pathRegex       *regexp.Regexp
	filenameRegex   *regexp.Regexp // to get the details of a package (arch, version etc)
	filenameDBRegex *regexp.Regexp // to get the filename from the db file
	mirrorlistRegex *regexp.Regexp // to extract the url from a mirrorlist file
	prefetchDB      *gorm.DB
)

// Accepted formats
var allowedPackagesExtensions []string

func init() {
	var err error
	pathRegex, err = regexp.Compile("^/repo/([^/]*)(/.*)?/([^/]*)$")
	if err != nil {
		panic(err)
	}
	// source: https://archlinux.org/pacman/makepkg.conf.5.html PKGEXT section, sorted with compressed formats as first.
	allowedPackagesExtensions = []string{".pkg.tar.zst", ".pkg.tar.gz", ".pkg.tar.xz", ".pkg.tar.bz2", ".pkg.tar.lzo", ".pkg.tar.lrz", ".pkg.tar.lz4", ".pkg.tar.lz", ".pkg.tar.Z", ".pkg.tar"}

	// Filename regex explanation (also here https://regex101.com/r/qB0fQ7/36 )
	/*
		The filename relevant matches are:
		^([a-z0-9._+-]+)			a package filename must be a combination of lowercase letters,numbers,dots, underscores, plus symbols or dashes
		-							separator
		([a-z0-9A-Z:._+]+-[0-9.]+)	epoch/version. an epoch can be written as (whatever)-(sequence of numbers with possibly dots)
		-							separator
		([a-zA-Z0-9:._+]+)			arch
		-							separator
		(([.]...)$					file extension, explanation below

			File extension explanation:
			(
				([.]pkg[.]tar		final file extension must start with .pkg.tar, then another suffix can be present
					(
						([.]gz)|	they are in disjunction with each other
						([.]bz2)|
						([.]xz)|
						([.]zst)|
						([.]lzo)|
						([.]lrz)|
						([.]lz4)|
						([.]lz)|
						([.]Z)
					)?				they are not mandatory
				)
				([.]sig)?			It could be a signature, so it could have a terminating .sig extension
			)$


	*/
	filenameRegex, err = regexp.Compile("^([a-z0-9._+-]+)-([a-zA-Z0-9:._+]+-[0-9.]+)-([a-zA-Z0-9:._+]+)(([.]pkg[.]tar(([.]gz)|([.]bz2)|([.]xz)|([.]zst)|([.]lzo)|([.]lrz)|([.]lz4)|([.]lz)|([.]Z))?)([.]sig)?)$")
	if err != nil {
		log.Fatal(err)
	} // shouldn't happen
	filenameDBRegex, err = regexp.Compile("[%]FILENAME[%]\n([^\n]+)\n")
	if err != nil {
		log.Fatal(err)
	} // shouldn't happen

	//	Analysis of the mirrorlistRegex regex (also here https://regex101.com/r/1oEit0/1):
	//  ^\s*Server\s*=\s*						Starts with `Server=` keyword, with optional spaces before and after `Server` and `=`
	//  ([^\s$]+)(\$[^\s]+)						Non white spaces and not $ characters composes the url, which must end with a $ string (e.g. `$repo/os/$arch`)
	//  [\s]*									Optional ending whitespaces
	//  (#.*)?									Optional comment starting with #

	mirrorlistRegex, err = regexp.Compile(`^\s*Server\s*=\s*([^\s$]+)(\$[^\s]+)[\s]*(#.*)?$`)
	if err != nil {
		log.Fatal(err)
	} // shouldn't happen
}

func main() {
	flag.Parse()
	log.SetFlags(log.Lshortfile)

	log.Print("Reading config file from ", *configFile)
	yaml, err := os.ReadFile(*configFile)
	if err != nil {
		log.Fatal(err)
	}
	config = parseConfig(yaml)
	if config.LogTimestamp == true {
		log.SetFlags(log.LstdFlags)
	}
	if config.Prefetch != nil {
		prefetchTicker := setupPrefetchTicker()
		defer prefetchTicker.Stop()
		setupPrefetch() // enable refresh
	}

	for repoName := range config.Repos {
		cachePath := filepath.Join(config.CacheDir, "pkgs", repoName)
		size, err := gatherCacheSize(cachePath)
		if err != nil {
			log.Println("Gathering size failed for ", repoName)
		}
		cacheSizeGauge.WithLabelValues(repoName).Set(size)
	}

	if config.PurgeFilesAfter != 0 {
		cleanupTicker := setupPurgeStaleFilesRoutine()
		defer cleanupTicker.Stop()
	}

	if config.HttpProxy != "" {
		proxyUrl, err := url.Parse(config.HttpProxy)
		if err != nil {
			log.Fatal(err)
		}
		http.DefaultTransport = &http.Transport{Proxy: http.ProxyURL(proxyUrl)}
	}

	if config.UserAgent == "" {
		config.UserAgent = "Pacoloco/1.2"
	}

	listenAddr := fmt.Sprintf(":%d", config.Port)
	log.Println("Starting server at port", config.Port)
	// The request path looks like '/repo/$reponame/$pathatmirror'
	http.HandleFunc("/repo/", pacolocoHandler)
	// Expose prometheus metrics
	http.Handle("/metrics", promhttp.Handler())
	log.Fatal(http.ListenAndServe(listenAddr, nil))
}

func gatherCacheSize(repoDir string) (float64, error) {
	var size int64
	err := filepath.Walk(repoDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			size += info.Size()
		}
		return err
	})
	return float64(size), err
}

func pacolocoHandler(w http.ResponseWriter, req *http.Request) {
	if err := handleRequest(w, req); err != nil {
		log.Println(err)
		w.WriteHeader(http.StatusNotFound)
	}
}

func forceCheckAtServer(fileName string) bool {
	// Suffixes for mutable files. We need to check the files modification date at the server.
	forceCheckFiles := []string{".db", ".db.sig", ".files"}

	for _, e := range forceCheckFiles {
		if strings.HasSuffix(fileName, e) {
			return true
		}
	}
	return false
}

var (
	cacheRequestsCounter = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "pacoloco_cache_requests_total",
		Help: "Number of requests to cache",
	}, []string{"repo"})
	cacheServedCounter = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "pacoloco_cache_hits_total",
		Help: "The total number of cache hits",
	}, []string{"repo"})
	cacheMissedCounter = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "pacoloco_cache_miss_total",
		Help: "The total number of cache misses",
	}, []string{"repo"})
	cacheServingFailedCounter = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "pacoloco_cache_errors_total",
		Help: "Number of errors while trying to serve cached file",
	}, []string{"repo"})

	cacheSizeGauge = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "pacoloco_cache_size_bytes",
		Help: "Number of bytes taken by the cache",
	}, []string{"repo"})

	// Track individual mirror behavior
	downloadedFilesCounter = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "pacoloco_downloaded_files_total",
		Help: "Total number of downloaded files",
	}, []string{"repo", "upstream", "status"})
)

// A mutex map for files currently being downloaded
// It is used to prevent downloading the same file with concurrent requests
var (
	downloadingFiles      = make(map[string]*sync.Mutex)
	downloadingFilesMutex sync.Mutex
)

// force resources prefetching
func prefetchRequest(url string, optionalCustomPath string) (err error) {
	urlPath := url
	matches := pathRegex.FindStringSubmatch(urlPath)
	if len(matches) == 0 {
		return fmt.Errorf("input url path '%v' does not match expected format", urlPath)
	}
	repoName := matches[1]
	path := matches[2]
	fileName := matches[3]
	repo, ok := config.Repos[repoName]
	if !ok {
		return fmt.Errorf("cannot find repo %s in the config file", repoName)
	}
	// create cache directory if needed
	cachePath := filepath.Join(config.CacheDir, "pkgs", repoName)
	if _, err := os.Stat(cachePath); os.IsNotExist(err) {
		if err := os.MkdirAll(cachePath, os.ModePerm); err != nil {
			return err
		}
	}
	var filePath string
	if optionalCustomPath != "" {
		filePath = filepath.Join(optionalCustomPath, fileName)
	} else {
		filePath = filepath.Join(cachePath, fileName)
	}
	// mandatory update when prefetching,

	mutexKey := repoName + ":" + fileName
	downloadingFilesMutex.Lock()
	fileMutex, ok := downloadingFiles[mutexKey]
	if !ok {
		fileMutex = &sync.Mutex{}
		downloadingFiles[mutexKey] = fileMutex
	}
	downloadingFilesMutex.Unlock()
	fileMutex.Lock()
	defer func() {
		fileMutex.Unlock()
		downloadingFilesMutex.Lock()
		delete(downloadingFiles, mutexKey)
		downloadingFilesMutex.Unlock()
	}()

	// refresh the data in case if the file has been download while we were waiting for the mutex
	ifLater := time.Time{} // spoofed to avoid rewriting downloadFile
	downloaded := false
	for _, url := range repo.getUrls() {
		downloaded, err = downloadFile(url+path+"/"+fileName, filePath, ifLater, nil)
		if downloaded {
			break
		}
	}

	if downloaded && config.Prefetch != nil {
		if !strings.HasSuffix(fileName, ".sig") && !strings.HasSuffix(fileName, ".db") {
			updateDBPrefetchedFile(repoName, fileName) // update info for prefetching
		}
	}

	if err == nil && !downloaded {
		err = fmt.Errorf("not downloaded")
	}

	return err
}

func handleRequest(w http.ResponseWriter, req *http.Request) error {
	urlPath := req.URL.Path
	matches := pathRegex.FindStringSubmatch(urlPath)
	if len(matches) == 0 {
		return fmt.Errorf("input url path '%v' does not match expected format", urlPath)
	}
	repoName := matches[1]
	path := matches[2]
	fileName := matches[3]
	repo, ok := config.Repos[repoName]
	if !ok {
		return fmt.Errorf("cannot find repo %s in the config file", repoName)
	}
	cacheRequestsCounter.WithLabelValues(repoName).Inc()
	// create cache directory if needed
	cachePath := filepath.Join(config.CacheDir, "pkgs", repoName)
	if _, err := os.Stat(cachePath); os.IsNotExist(err) {
		if err := os.MkdirAll(cachePath, os.ModePerm); err != nil {
			return err
		}
	}

	filePath := filepath.Join(cachePath, fileName)
	stat, err := os.Stat(filePath)
	noFile := err != nil
	requestFromServer := noFile || forceCheckAtServer(fileName)

	if requestFromServer {
		mutexKey := repoName + ":" + fileName
		downloadingFilesMutex.Lock()
		fileMutex, ok := downloadingFiles[mutexKey]
		if !ok {
			fileMutex = &sync.Mutex{}
			downloadingFiles[mutexKey] = fileMutex
		}
		downloadingFilesMutex.Unlock()
		fileMutex.Lock()
		defer func() {
			fileMutex.Unlock()
			downloadingFilesMutex.Lock()
			delete(downloadingFiles, mutexKey)
			downloadingFilesMutex.Unlock()
		}()

		// refresh the data in case if the file has been download while we were waiting for the mutex
		stat, err = os.Stat(filePath)
		noFile = err != nil
		requestFromServer = noFile || forceCheckAtServer(fileName)
	}

	var downloaded bool
	if requestFromServer {
		ifLater, _ := http.ParseTime(req.Header.Get("If-Modified-Since"))
		if noFile {
			// ignore If-Modified-Since and download file if it does not exist in the cache
			ifLater = time.Time{}
		} else if stat.ModTime().After(ifLater) {
			ifLater = stat.ModTime()
		}

		for _, url := range repo.getUrls() {
			downloaded, err = downloadFile(url+path+"/"+fileName, filePath, ifLater, w)
			if err == nil {
				break
			}
		}
	}

	if !downloaded {
		log.Printf("serving cached file %v", filePath)
		if _, pathErr := os.Stat(filePath); pathErr == nil {
			cacheServedCounter.WithLabelValues(repoName).Inc()
		} else if os.IsNotExist(pathErr) {
			cacheServingFailedCounter.WithLabelValues(repoName).Inc()
		}
		http.ServeFile(w, req, filePath)
	}
	if downloaded {
		cacheMissedCounter.WithLabelValues(repoName).Inc()
		info, _ := os.Stat(filePath)
		cacheSizeGauge.WithLabelValues(repoName).Add(float64(info.Size()))
	}

	if downloaded && config.Prefetch != nil {
		if !strings.HasSuffix(fileName, ".sig") && !strings.HasSuffix(fileName, ".db") {
			updateDBRequestedFile(repoName, fileName) // update info for prefetching
		} else if strings.HasSuffix(fileName, ".db") {
			updateDBRequestedDB(repoName, path, fileName)
		}
	}
	return err
}

// downloadFileAndSend downloads file from `url`, saves it to the given `localFileName`
// file and sends to `clientWriter` at the same time.
// The function returns whether the function sent the data to client and error if one occurred
func downloadFile(upstream_url string, filePath string, ifModifiedSince time.Time, clientWriter http.ResponseWriter) (downloaded bool, err error) {
	var req *http.Request

	u, err := url.Parse(upstream_url)
	if err != nil {
		return
	}

	host := u.Host
	if config.DownloadTimeout > 0 {
		ctx, ctxCancel := context.WithTimeout(context.Background(), time.Duration(config.DownloadTimeout)*time.Second)
		defer ctxCancel()
		req, err = http.NewRequestWithContext(ctx, "GET", upstream_url, nil)
	} else {
		req, err = http.NewRequest("GET", upstream_url, nil)
	}
	if err != nil {
		return
	}

	if !ifModifiedSince.IsZero() {
		req.Header.Set("If-Modified-Since", ifModifiedSince.UTC().Format(http.TimeFormat))
	}
	// golang requests compression for all requests except HEAD
	// some servers return compressed data without Content-Length header info
	// disable compression as it useless for package data
	req.Header.Add("Accept-Encoding", "identity")
	req.Header.Set("User-Agent", config.UserAgent)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	repoName := filepath.Base(filepath.Dir(filePath))
	downloadedFilesCounter.WithLabelValues(repoName, host, strconv.Itoa(resp.StatusCode)).Inc()

	switch resp.StatusCode {
	case http.StatusOK:
		break
	case http.StatusNotModified:
		// either pacoloco or client has the latest version, no need to redownload it
		return
	default:
		// for most dbs signatures are optional, be quiet if the signature is not found
		// quiet := resp.StatusCode == http.StatusNotFound && strings.HasSuffix(url, ".db.sig")
		err = fmt.Errorf("unable to download url %s, status code is %d", upstream_url, resp.StatusCode)
		return
	}

	file, err := os.Create(filePath)
	if err != nil {
		return
	}
	var w io.Writer
	w = file

	log.Printf("downloading %v", upstream_url)
	if clientWriter != nil {
		clientWriter.Header().Set("Content-Length", resp.Header.Get("Content-Length"))
		clientWriter.Header().Set("Content-Type", "application/octet-stream")
		clientWriter.Header().Set("Last-Modified", resp.Header.Get("Last-Modified"))
		w = io.MultiWriter(w, clientWriter)
	}

	_, err = io.Copy(w, resp.Body)
	_ = file.Close() // Close the file early to make sure the file modification time is set
	if err != nil {
		// remove the cached file if download was not successful
		log.Print(err)
		_ = os.Remove(filePath)
		return
	}
	downloaded = true

	if lastModified := resp.Header.Get("Last-Modified"); lastModified != "" {
		lastModified, parseErr := http.ParseTime(lastModified)
		err = parseErr
		if err == nil {
			if err = os.Chtimes(filePath, time.Now(), lastModified); err != nil {
				return
			}
		}
	}

	return
}
