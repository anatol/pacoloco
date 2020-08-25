package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var configFile = flag.String("config", "/etc/pacoloco.yaml", "Path to config file")
var pathRegex *regexp.Regexp

func init() {
	var err error
	pathRegex, err = regexp.Compile("^/repo/([^/]*)(/.*)?/([^/]*)$")
	if err != nil {
		panic(err)
	}
}

func main() {
	flag.Parse()
	log.SetFlags(log.Lshortfile)

	log.Print("Reading config file from ", *configFile)
	config = readConfig(*configFile)

	cleanupTicker := setupPurgeStaleFilesRoutine()
	defer cleanupTicker.Stop()

	listenAddr := fmt.Sprintf(":%d", config.Port)
	log.Println("Starting server at port", config.Port)
	// The request path looks like '/repo/$reponame/$pathatmirror'
	http.HandleFunc("/repo/", pacolocoHandler)
	// http.HandleFunc("/stats", statsHandler) TODO: implement stats
	log.Fatal(http.ListenAndServe(listenAddr, nil))
}

func pacolocoHandler(w http.ResponseWriter, req *http.Request) {
	err := handleRequest(w, req)
	if err != nil {
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

func handleRequest(w http.ResponseWriter, req *http.Request) error {
	urlPath := req.URL.Path
	matches := pathRegex.FindStringSubmatch(urlPath)
	if matches == nil {
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
		err := os.MkdirAll(cachePath, os.ModePerm)
		if err != nil {
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
		_, ok = downloadingFiles[mutexKey]
		if !ok {
			downloadingFiles[mutexKey] = &sync.Mutex{}
		}
		fileMutex := downloadingFiles[mutexKey]
		downloadingFilesMutex.Unlock()
		fileMutex.Lock()
		defer func() {
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

		if repo.Url != "" {
			err, served = downloadFile(repo.Url+path+"/"+fileName, filePath, ifLater, w)
		} else {
			for _, url := range repo.Urls {
				err, served = downloadFile(url+path+"/"+fileName, filePath, ifLater, w)
				if err == nil {
					break
				}
			}
		}
	}
	if !served {
		err = sendCachedFile(w, req, fileName, filePath)
	}
	return err
}

// downloadFile downloads file from `url`, saves it to the given `localFileName`
// file and sends to `clientWriter` at the same time.
// The function returns whether the function sent the data to client and error if one occurred
func downloadFile(url string, filePath string, ifModifiedSince time.Time, clientWriter http.ResponseWriter) (err error, served bool) {
	ctx, ctxCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer ctxCancel()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
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
			err = os.Chtimes(filePath, time.Now(), lastModified)
			if err != nil {
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

	http.ServeContent(w, req, fileName, stat.ModTime(), file)
	return nil
}
