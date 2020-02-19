package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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
	if noFile || forceCheckAtServer(fileName) {
		ifLater, _ := http.ParseTime(req.Header.Get("If-Modified-Since"))
		if noFile {
			// ignore If-Modified-Since and download file if it does not exist in the cache
			ifLater = time.Time{}
		} else if stat.ModTime().After(ifLater) {
			ifLater = stat.ModTime()
		}

		if repo.Url != "" {
			err = downloadFile(repo.Url+path+"/"+fileName, filePath, ifLater)
		} else {
			for _, url := range repo.Urls {
				err = downloadFile(url+path+"/"+fileName, filePath, ifLater)
				if err == nil {
					break
				}
			}
		}
		if err != nil {
			return err
		}
	}

	return sendFile(w, req, fileName, filePath)
}

// downloadFile downloads file from `url` and saves it with given `localFileName`
func downloadFile(url string, filePath string, ifModifiedSince time.Time) error {
	// TODO: add a mutex that prevents multiple downloads for the same file

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}

	if !ifModifiedSince.IsZero() {
		req.Header.Set("If-Modified-Since", ifModifiedSince.UTC().Format(http.TimeFormat))
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		break
	case http.StatusNotModified:
		// either pacoloco or client has the latest version, no need to redownload it
		return nil
	default:
		// for most dbs signatures are optional, silent if the signature is not found
		// silent := resp.StatusCode == http.StatusNotFound && strings.HasSuffix(url, ".db.sig")
		return fmt.Errorf("unable to download url %s, status code is %d", url, resp.StatusCode)
	}

	out, err := os.Create(filePath)
	if err != nil {
		return err
	}

	log.Printf("downloading %v", url)
	_, err = io.Copy(out, resp.Body) // TODO: if multiple clients requested this file then make resp.Body streaming to all the clients
	_ = out.Close()                  // Close the file early to make sure the file modification time is set
	if err != nil {
		return err
	}

	if lastModified := resp.Header.Get("Last-Modified"); lastModified != "" {
		lastModified, err := http.ParseTime(lastModified)
		if err == nil {
			err = os.Chtimes(filePath, lastModified, lastModified)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func sendFile(w http.ResponseWriter, req *http.Request, fileName string, filePath string) error {
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
