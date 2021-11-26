package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
)

var configFile = flag.String("config", "/etc/pacoloco.yaml", "Path to config file")

var pathRegex *regexp.Regexp
var filenameRegex *regexp.Regexp   // to get the details of a package (arch, version etc)
var filenameDBRegex *regexp.Regexp // to get the filename from the db file
var urlRegex *regexp.Regexp        // to extract the relevant parts from an url to compose a pacoloco url
var mirrorDBRegex *regexp.Regexp   // to extract the "path" field from a url
var prefetchDB *gorm.DB

// Accepted formats
var allowedPackagesExtensions []string

func init() {
	var err error
	pathRegex, err = regexp.Compile("^/repo/([^/]*)(/.*)?/([^/]*)$")
	if err != nil {
		panic(err)
	}
	// source: https://archlinux.org/pacman/makepkg.conf.5.html PKGEXT section
	allowedPackagesExtensions = []string{".pkg.tar.gz", ".pkg.tar.bz2", ".pkg.tar.xz", ".pkg.tar.zst", ".pkg.tar.lzo", ".pkg.tar.lrz", ".pkg.tar.lz4", ".pkg.tar.lz", ".pkg.tar.Z", ".pkg.tar"}

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
	/*
		Analysis of the regex:*/
	// 	"^([a-zA-Z0-9+_-]+://[^/]+)	captures httpwhatever://baseurl.tld, which has to be discarded
	// 	/(([^/]*/)+)				captures in group 2 the whole set of word/another/one/etc/, discarding the leading /
	// 								group3 is useless
	// 	([^/]+)$"					group4 is the filename which has to be replaced

	urlRegex, err = regexp.Compile("^([a-zA-Z0-9+_-]+://[^/]+)/(([^/]*/)+)([^/]+)$")
	if err != nil {
		log.Fatal(err)
	} // shouldn't happen
	// Starting from a string like "///extra/os/x86_64/extra.db" , it matches "///extra/os/x86_64/"
	// More details here https://regex101.com/r/kMGOhq/1
	mirrorDBRegex, err = regexp.Compile("^/*([^/]+/+)+")
	if err != nil {
		log.Fatal(err)
	} // shouldn't happen
}

func main() {
	flag.Parse()
	log.SetFlags(log.Lshortfile)

	log.Print("Reading config file from ", *configFile)
	yaml, err := ioutil.ReadFile(*configFile)
	if err != nil {
		log.Fatal(err)
	}
	config = parseConfig(yaml)
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

// A mutex map for files currently being downloaded
// It is used to prevent downloading the same file with concurrent requests
var (
	downloadingFiles      = make(map[string]*sync.Mutex)
	downloadingFilesMutex sync.Mutex
)

// force resources prefetching
func prefetchRequest(url string) (err error) {
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
	filePath := filepath.Join(cachePath, fileName)
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
	if repo.URL != "" {
		downloaded, err = downloadFile(repo.URL+path+"/"+fileName, filePath, ifLater)
		if err == nil && config.Prefetch != nil && !strings.HasSuffix(fileName, ".sig") && !strings.HasSuffix(fileName, ".db") {
			updateDBPrefetchedFile(repoName, fileName) // update info for prefetching
		}
	} else {
		for _, url := range repo.URLs {
			downloaded, err = downloadFile(url+path+"/"+fileName, filePath, ifLater)
			if err == nil {
				if config.Prefetch != nil && !strings.HasSuffix(fileName, ".sig") && !strings.HasSuffix(fileName, ".db") {
					updateDBPrefetchedFile(repoName, fileName) // update info for prefetching
				}
				break
			}
		}
	}
	if err != nil {
		return err
	} else if !downloaded {
		return fmt.Errorf("not downloaded")
	} else {
		return nil
	}
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

	var served bool
	if requestFromServer {
		ifLater, _ := http.ParseTime(req.Header.Get("If-Modified-Since"))
		if noFile {
			// ignore If-Modified-Since and download file if it does not exist in the cache
			ifLater = time.Time{}
		} else if stat.ModTime().After(ifLater) {
			ifLater = stat.ModTime()
		}

		if repo.URL != "" {
			served, err = downloadFileAndSend(repo.URL+path+"/"+fileName, filePath, ifLater, w)
			if err == nil && config.Prefetch != nil && !strings.HasSuffix(fileName, ".sig") && !strings.HasSuffix(fileName, ".db") {
				updateDBDownloadedFile(repoName, fileName) // update info for prefetching
			} else if err == nil && config.Prefetch != nil && strings.HasSuffix(fileName, ".db") {
				addDBfileToDB(repo.URL+path+"/"+fileName, repoName)
			}
		} else {
			for _, url := range repo.URLs {
				served, err = downloadFileAndSend(url+path+"/"+fileName, filePath, ifLater, w)
				if err == nil {
					if config.Prefetch != nil && !strings.HasSuffix(fileName, ".sig") && !strings.HasSuffix(fileName, ".db") {
						updateDBDownloadedFile(repoName, fileName) // update info for prefetching
					} else if err == nil && config.Prefetch != nil && strings.HasSuffix(fileName, ".db") {
						addDBfileToDB(url+path+"/"+fileName, repoName)
					}
					break
				}
			}
		}

	}
	if !served {
		err = sendCachedFile(w, req, fileName, filePath)
		if err == nil && config.Prefetch != nil && !strings.HasSuffix(fileName, ".sig") && !strings.HasSuffix(fileName, ".db") {
			updateDBDownloadedFile(repoName, fileName) // update info for prefetching
		}
	}
	return err
}

// downloadFile downloads file from `url`, saves it to the given `localFileName`
// file.
// The function returns whether the function saved the downloaded data and error if one occurred
func downloadFile(url string, filePath string, ifModifiedSince time.Time) (served bool, err error) {
	var req *http.Request
	if config.DownloadTimeout > 0 {
		ctx, ctxCancel := context.WithTimeout(context.Background(), time.Duration(config.DownloadTimeout)*time.Second)
		defer ctxCancel()
		req, err = http.NewRequestWithContext(ctx, "GET", url, nil)
	} else {
		req, err = http.NewRequest("GET", url, nil)
	}
	if err != nil {
		return false, err
	}

	if !ifModifiedSince.IsZero() {
		req.Header.Set("If-Modified-Since", ifModifiedSince.UTC().Format(http.TimeFormat))
	}
	// golang requests compression for all requests except HEAD
	// some servers return compressed data without Content-Length header info
	// disable compression as it useless for package data
	req.Header.Add("Accept-Encoding", "identity")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
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
	w := io.Writer(file)
	log.Printf("downloading %v", url)
	_, err = io.Copy(w, resp.Body)
	_ = file.Close() // Close the file early to make sure the file modification time is set
	if err != nil {
		// remove the cached file if download was not successful
		log.Print(err)
		_ = os.Remove(filePath)
		return
	}
	served = true

	if lastModified := resp.Header.Get("Last-Modified"); lastModified != "" {
		lastModified, parseErr := http.ParseTime(lastModified)
		if err = parseErr; err == nil {
			if err = os.Chtimes(filePath, time.Now(), lastModified); err != nil {
				return true, nil
			}
		}
	}
	return served, nil
}

// downloadFileAndSend downloads file from `url`, saves it to the given `localFileName`
// file and sends to `clientWriter` at the same time.
// The function returns whether the function sent the data to client and error if one occurred
func downloadFileAndSend(url string, filePath string, ifModifiedSince time.Time, clientWriter http.ResponseWriter) (served bool, err error) {
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
		return
	default:
		// for most dbs signatures are optional, be quiet if the signature is not found
		// quiet := resp.StatusCode == http.StatusNotFound && strings.HasSuffix(url, ".db.sig")
		err = fmt.Errorf("unable to download url %s, status code is %d", url, resp.StatusCode)
		return
	}

	file, err := os.Create(filePath)
	if err != nil {
		return
	}

	log.Printf("downloading %v", url)
	clientWriter.Header().Set("Content-Length", resp.Header.Get("Content-Length"))
	clientWriter.Header().Set("Content-Type", "application/octet-stream")
	clientWriter.Header().Set("Last-Modified", resp.Header.Get("Last-Modified"))

	w := io.MultiWriter(file, clientWriter)
	_, err = io.Copy(w, resp.Body)
	_ = file.Close() // Close the file early to make sure the file modification time is set
	if err != nil {
		// remove the cached file if download was not successful
		log.Print(err)
		_ = os.Remove(filePath)
		return
	}
	served = true

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

func sendCachedFile(w http.ResponseWriter, req *http.Request, fileName string, filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return err
	}
	log.Printf("serving cached file %v", filePath)
	http.ServeContent(w, req, fileName, stat.ModTime(), file)
	return nil
}
