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
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
)

var configFile = flag.String("config", "/etc/pacoloco.yaml", "Path to config file")

var (
	pathRegex       *regexp.Regexp // to check if a URL request's path is valid
	rootPathRegex   *regexp.Regexp // to check if a URL request's path is valid for a repo root directory
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
	// Useful to check if a pacoloco repository is available
	rootPathRegex, err = regexp.Compile("^/repo/([^/]*)/?$")
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
		config.UserAgent = "Pacoloco/1.2.1"
	}

	listenAddr := fmt.Sprintf(":%d", config.Port)
	log.Println("Starting server at port", config.Port)
	// The request path looks like '/repo/$reponame/$pathatmirror'
	http.HandleFunc("/repo/", pacolocoHandler)
	// http.HandleFunc("/stats", statsHandler) TODO: implement stats
	log.Fatal(http.ListenAndServe(listenAddr, nil))
}

func pacolocoHandler(w http.ResponseWriter, req *http.Request) {
	if err := handleRequest(w, req); err != nil {
		log.Println(err)
		// w.WriteHeader(http.StatusNotFound) // no more neeeded
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

// A mutex map for files currently being downloaded
// It is used to prevent downloading the same file with concurrent requests
var (
	downloadingFiles      = make(map[string]bool)
	downloadingFilesMutex sync.RWMutex
)

// force resources prefetching
func prefetchRequest(url string, optionalCustomPath string) (err error) {
	urlPath := url
	matches := pathRegex.FindStringSubmatch(path.Clean(urlPath))
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
	dummyIfLater := time.Time{} // Not important here
	// ---------------------------- Begin mutex code ----------------------------
	mutexKey := filePath
	// Check files and the map with a read lock on the map,
	// to guarantee that it is being kept in a safe status throughout those checks
	downloadingFilesMutex.RLock()
	_, isBeingDownloaded := downloadingFiles[mutexKey]
	// Start locking this file's mutex if the file has to be downloaded
	if !isBeingDownloaded {
		// Edit the map by saying that I am downloading this file
		downloadingFilesMutex.RUnlock()                    // There is no atomic way to promote a R lock to a W lock, so we need to unlock and lock again
		downloadingFilesMutex.Lock()                       // Exclusively locked
		_, isBeingDownloaded := downloadingFiles[mutexKey] // if someone else is downloading it now, I cannot download it
		if !isBeingDownloaded {
			downloadingFiles[mutexKey] = true
		}
		downloadingFilesMutex.Unlock()
	} else {
		downloadingFilesMutex.RUnlock() // unlock the read lock
	}
	// ---------------------------- End mutex code ----------------------------
	if isBeingDownloaded {
		// Someone else is downloading this file, so I cannot download it. I can skip it, in the assumption that the other thread will download it
		// won't stop the download
		return fmt.Errorf("file %s in repo %s is already being downloaded, there is no point in prefetching it", fileName, repoName)
	} else {
		urls := repo.getUrls()
		if len(urls) > 0 {
			var lastErr error
			for _, url := range urls {
				statusHeadReq, err := checkUpstreamFileAvailability(url+path+"/"+fileName, dummyIfLater)
				if shouldSendRequest(statusHeadReq) {
					downloaded, err := downloadFile(url+path+"/"+fileName, filePath, dummyIfLater, nil)
					if err == nil && config.Prefetch != nil && !strings.HasSuffix(fileName, ".sig") && !strings.HasSuffix(fileName, ".db") {
						updateDBPrefetchedFile(repoName, fileName) // update info for prefetching
						return nil
					} else if err != nil {
						return err
					} else if config.Prefetch == nil {
						return fmt.Errorf("prefetching is not enabled in the config file but the file had been downloaded")
					} else if !downloaded {
						return fmt.Errorf("no error occurred but file %s had not prefetched", fileName)
					} else {
						// The file had been successfully prefetched and it is either a .sig or a .db file, so we can end here
						return nil
					}
				} else if statusHeadReq == http.StatusNotModified && err == nil {
					err = fmt.Errorf("the server %s replied 'Not Modified' with a default if-modified-since parameter! Considering %s as not available from here", url, fileName)
				} // otherwise, try the next mirror
				lastErr = err
			}
			if lastErr != nil {
				return lastErr
			} else {
				return fmt.Errorf("file %s not available upstream in all repos", fileName)
			}
		} else {
			return fmt.Errorf("no repo URL is available to satisfy your prefetch request")
		}
	}
}

func handleRequest(w http.ResponseWriter, req *http.Request) error {
	urlPath := req.URL.Path
	matches := pathRegex.FindStringSubmatch(path.Clean(urlPath))
	if len(matches) == 0 {
		// Check if it was a check for a repo root
		rootMatch := rootPathRegex.FindStringSubmatch(path.Clean(urlPath))
		if len(rootMatch) != 0 {
			repoName := rootMatch[1] // the filename should fall in
			_, ok := config.Repos[repoName]
			if ok {
				w.WriteHeader(http.StatusOK)
				return nil
			}
		}
		w.WriteHeader(http.StatusForbidden)
		return fmt.Errorf("input url path '%v' does not match expected format", urlPath)
	}
	repoName := matches[1]
	path := matches[2]
	fileName := matches[3]
	repo, ok := config.Repos[repoName]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return fmt.Errorf("cannot find repo %s in the config file", repoName)
	}
	if fileName == "" && config.Repos[repoName] != nil {
		// A request to the repo root should simply return a 200 OK with no listing
		w.WriteHeader(http.StatusOK)
		return nil
	}

	// create cache directory if needed
	cachePath := filepath.Join(config.CacheDir, "pkgs", repoName)
	if _, cacheError := os.Stat(cachePath); os.IsNotExist(cacheError) {
		if err := os.MkdirAll(cachePath, os.ModePerm); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return err
		}
	}
	filePath := filepath.Join(cachePath, fileName)
	// Check files and the map with a write lock on the map, to guarantee that it is being kept
	//in a safe status throughout those checks in which we establish what it has to be done.
	// A more restrictive write lock has to be used to specify what we are planning to download
	hasRangeHeader := req.Header.Get("Range") != ""
	mutexKey := filePath
	downloadingFilesMutex.Lock()
	cachedFileStat, cacheStatErr := os.Stat(filePath)
	existsCachedFile := cacheStatErr == nil
	ifModifiedSince, ifModifiedSinceParsingError := http.ParseTime(req.Header.Get("If-Modified-Since"))
	shouldForceCheck := forceCheckAtServer(fileName)
	_, beingDownloaded := downloadingFiles[mutexKey]
	existsFullyCachedFile := !beingDownloaded && existsCachedFile // Assume that if there is no mutex, the file is fully cached
	hasToBeDownloaded := (shouldForceCheck || !existsFullyCachedFile) && !beingDownloaded && !hasRangeHeader
	if hasToBeDownloaded {
		downloadingFiles[mutexKey] = true
		defer func() {
			downloadingFilesMutex.Lock()
			delete(downloadingFiles, mutexKey)
			downloadingFilesMutex.Unlock()
		}()
	}
	downloadingFilesMutex.Unlock()

	// Early serve the request if the file is fully cached and the client has the updated file
	if ifModifiedSinceParsingError == nil && existsFullyCachedFile && !shouldForceCheck {
		if ifModifiedSince.After(cachedFileStat.ModTime()) || ifModifiedSince.Equal(cachedFileStat.ModTime()) {
			w.WriteHeader(http.StatusNotModified)
			return nil
		}
	}
	// Set the ifModifiedSince parameter if unset
	if ifModifiedSinceParsingError != nil {
		ifModifiedSince = time.Time{}
	}
	if existsFullyCachedFile && !shouldForceCheck { // Serve the local file if it exists
		// We could check the if-modified-since header here, but we don't need to
		// since file names are assumed to be unique
		log.Printf("serving cached file %v", filePath)
		http.ServeFile(w, req, filePath)
		if config.Prefetch != nil && !strings.HasSuffix(fileName, ".sig") && !strings.HasSuffix(fileName, ".db") {
			updateDBRequestedFile(repoName, fileName) // update info for prefetching
		} else if strings.HasSuffix(fileName, ".db") {
			updateDBRequestedDB(repoName, path, fileName)
		}
	} else if hasRangeHeader || beingDownloaded {
		// If the file is an unhandled range request (unhandled because it is not cached) or it is being downloaded,
		// redirect to the upstream repo
		urls := repo.getUrls()
		if len(urls) > 0 {
			var latestErr error
			for _, url := range urls {
				statusHeadReq, err := checkUpstreamFileAvailability(url+path+"/"+fileName, ifModifiedSince)
				if shouldSendRequest(statusHeadReq) && err == nil { // file exists on the server or the server refuses HEAD requests
					log.Default().Printf("Redirecting to %s", url+path+"/"+fileName)
					http.Redirect(w, req, url+path+"/"+fileName, http.StatusFound)
					if config.Prefetch != nil && !strings.HasSuffix(fileName, ".sig") && !strings.HasSuffix(fileName, ".db") {
						updateDBRequestedFile(repoName, fileName) // update info for prefetching
					} else if config.Prefetch != nil && strings.HasSuffix(fileName, ".db") {
						updateDBRequestedDB(repoName, path, fileName)
					}
					return nil
				} // if an error occurs, skip this mirror url and try the next one
				latestErr = err
			}
			if latestErr == nil {
				w.WriteHeader(http.StatusNotFound)
				return fmt.Errorf("the file %s is not available upstream on repo %s, aborted redirection", fileName, repoName)
			} else {
				w.WriteHeader(http.StatusNotFound) // None of the mirrors have the file
				return fmt.Errorf("the file %s is not available upstream or repo %s, download failed on range/concurrent request, error %s", fileName, repoName, latestErr)
			}
		} else {
			w.WriteHeader(http.StatusInternalServerError)
			return fmt.Errorf("no upstream mirror found for repo %s", repoName)
		}
	}

	if !hasToBeDownloaded {
		w.WriteHeader(http.StatusInternalServerError)
		return fmt.Errorf("this is a bug: It should never happen that the file that is going to be downloaded shouldn't be downloaded")
	}
	// Otherwise download the file and serve it
	// It should happen only if (shouldForceCheck || !existsFullyCachedFile) && !beingDownloaded && !hasRangeHeader
	urls := repo.getUrls()
	if len(urls) > 0 {
		var downloadErr error
		var statusHeadReq int
		downloadErr = nil
		downloaded := false
		for _, url := range urls {
			statusHeadReq, downloadErr = checkUpstreamFileAvailability(url+path+"/"+fileName, ifModifiedSince)
			if statusHeadReq == http.StatusNotModified && downloadErr == nil {
				// We're done but it should not happen, as we checked before
				w.WriteHeader(http.StatusNotModified)
				return nil
			}
			if shouldSendRequest(statusHeadReq) && downloadErr == nil {
				downloaded, downloadErr = downloadFile(url+path+"/"+fileName, filePath, ifModifiedSince, w)
				if downloadErr == nil {
					if config.Prefetch != nil && !strings.HasSuffix(fileName, ".sig") && !strings.HasSuffix(fileName, ".db") {
						updateDBRequestedFile(repoName, fileName) // update info for prefetching
					} else if downloadErr == nil && config.Prefetch != nil && strings.HasSuffix(fileName, ".db") {
						updateDBRequestedDB(repoName, path, fileName)
					}
					if downloaded {
						return nil
					} else {
						w.WriteHeader(http.StatusInternalServerError) // probably superfluous
						return fmt.Errorf("this is a bug: %s had been requested, downloaded but not served", url+path+"/"+fileName)
					}
				}
			} // if it shouldn't send the request to this mirror, so try the next one if it exists
		}
		if downloadErr != nil {
			w.WriteHeader(http.StatusNotFound)
			return downloadErr
		} else {
			w.WriteHeader(http.StatusNotFound)
			return fmt.Errorf("the file %s is not available upstream or repo %s, download failed", fileName, repoName)
		}
	}
	// else
	w.WriteHeader(http.StatusInternalServerError)
	return fmt.Errorf("no upstream mirror found for repo %s", repoName)
}

// downloadFile downloads file from `url`, saves it to the given `localFileName`
// file and sends to `clientWriter` at the same time.
// The function returns whether the function sent the data to client and error if one occurred
func downloadFile(url string, filePath string, ifModifiedSince time.Time, clientWriter http.ResponseWriter) (downloaded bool, err error) {
	var req *http.Request
	if config.DownloadTimeout > 0 {
		ctx, ctxCancel := context.WithTimeout(context.Background(), time.Duration(config.DownloadTimeout)*time.Second)
		defer ctxCancel()
		req, err = http.NewRequestWithContext(ctx, "GET", url, nil)
	} else {
		req, err = http.NewRequest("GET", url, nil)
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

	switch resp.StatusCode {
	case http.StatusOK:
		break
	case http.StatusNotModified:
		// either pacoloco or client has the latest version, no need to redownload it
		return false, nil
	default:
		// for most dbs signatures are optional, be quiet if the signature is not found
		// quiet := resp.StatusCode == http.StatusNotFound && strings.HasSuffix(url, ".db.sig")
		err = fmt.Errorf("unable to download url %s, status code is %d", url, resp.StatusCode)
		return false, err
	}

	file, err := os.Create(filePath)
	if err != nil {
		return
	}
	var w io.Writer
	w = file

	log.Printf("downloading %v", url)
	if clientWriter != nil {
		clientWriter.Header().Set("Content-Length", resp.Header.Get("Content-Length"))
		clientWriter.Header().Set("Content-Type", "application/octet-stream")
		clientWriter.Header().Set("Last-Modified", resp.Header.Get("Last-Modified"))
		clientWriter.Header().Set("Accept-Ranges", "bytes")
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

// Sends a HEAD request to check if the file is available on that url.
// Returns the status code, config.SkipHeadCheck is set or the head method is not supported.
func checkUpstreamFileAvailability(url string, ifModifiedSince time.Time) (statusCode int, err error) {
	if config == nil {
		return http.StatusBadGateway, fmt.Errorf("config is not initialized")
	}
	if config.SkipHeadCheck {
		return http.StatusOK, nil
	}
	log.Default().Printf("Checking upstream availability of %s ...", url)
	timeout := time.Duration(1000) * time.Millisecond
	if config.FileAvailabilityTimeout > 0 {
		timeout = time.Duration(config.DownloadTimeout) * time.Millisecond
	}
	ctx, ctxCancel := context.WithTimeout(context.Background(), timeout)
	defer ctxCancel()
	req, err := http.NewRequestWithContext(ctx, "HEAD", url, nil)
	if !ifModifiedSince.IsZero() {
		req.Header.Set("If-Modified-Since", ifModifiedSince.UTC().Format(http.TimeFormat))
	}
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		defer resp.Body.Close()
		return resp.StatusCode, err
	}
	return -1, err
}

func shouldSendRequest(statusHeadReq int) bool {
	return statusHeadReq == http.StatusOK ||
		statusHeadReq == http.StatusFound || // accept redirects
		statusHeadReq == http.StatusMethodNotAllowed || // head request is not supported
		statusHeadReq == http.StatusTemporaryRedirect ||
		statusHeadReq == http.StatusPermanentRedirect || // Almost unused, but just in case
		statusHeadReq == http.StatusPartialContent // Should not happen, but just in case
}
