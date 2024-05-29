package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	downloaders      = make(map[string]*Downloader) // currently active downloaders; the key of the map is urlPath
	downloadersMutex sync.Mutex
)

type Downloader struct {
	key            string // repoName + path + filename
	outputFileName string
	bufferFile     *os.File

	// downloaded metadata
	modificationTime time.Time
	contentLength    int

	repoName string
	repo     *Repo
	urlPath  string // path + filename

	usageCount atomic.Int32 // number of users that keep using the Downloader

	eventCond             *sync.Cond // sync point for receiving events, such as (error, metadataReceived, dataReceived, done)
	eventError            error
	eventMetadataReceived bool
	eventNotModified      bool
	eventDone             bool
	eventDataReceivedSize int
}

func (d *Downloader) decrementUsage() {
	val := d.usageCount.Add(-1)

	if val == 0 {
		downloadersMutex.Lock()
		defer downloadersMutex.Unlock()

		delete(downloaders, d.key)
		_ = d.bufferFile.Close()
		_ = os.Remove(d.bufferFile.Name())
	}
}

func (d *Downloader) download() error {
	urls := d.repo.getUrls()
	if len(urls) == 0 {
		return fmt.Errorf("repo %v has no urls", d.repoName)
	}

	var proxyURL *url.URL
	if d.repo.HttpProxy != "" {
		proxyURL, _ = url.Parse(d.repo.HttpProxy)
	} else {
		proxyURL = nil
	}

	for _, u := range urls {
		err := d.downloadFromUpstream(u, proxyURL)
		if err != nil {
			log.Printf("unable to download file %v: %v", d.key, err)
			continue // try next mirror
		}
		return nil
	}
	return fmt.Errorf("unable to download file %v", d.key)
}

func (d *Downloader) downloadFromUpstream(repoURL string, proxyURL *url.URL) error {
	upstreamURL := repoURL + d.urlPath

	var req *http.Request
	var err error
	if config.DownloadTimeout > 0 {
		ctx, ctxCancel := context.WithTimeout(context.Background(), time.Duration(config.DownloadTimeout)*time.Second)
		defer ctxCancel()
		req, err = http.NewRequestWithContext(ctx, "GET", upstreamURL, nil)
	} else {
		req, err = http.NewRequest("GET", upstreamURL, nil)
	}
	if err != nil {
		return err
	}

	if stat, err := os.Stat(d.outputFileName); err == nil {
		req.Header.Set("If-Modified-Since", stat.ModTime().UTC().Format(http.TimeFormat))
	}

	// golang requests compression for all requests except HEAD
	// some servers return compressed data without Content-Length header info
	// disable compression as it useless for package data
	req.Header.Add("Accept-Encoding", "identity")
	req.Header.Set("User-Agent", config.UserAgent)

	log.Printf("downloading %v", upstreamURL)

	var client *http.Client
	if proxyURL != nil {
		proxy := http.ProxyURL(proxyURL)
		transport := &http.Transport{Proxy: proxy}
		client = &http.Client{Transport: transport}
	} else {
		client = http.DefaultClient
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	downloadedFilesCounter.WithLabelValues(d.repoName, req.Host, strconv.Itoa(resp.StatusCode)).Inc()

	switch resp.StatusCode {
	case http.StatusOK:
		break
	case http.StatusNotModified:
		d.eventCond.L.Lock()
		d.eventNotModified = true
		d.eventCond.Broadcast()
		d.eventCond.L.Unlock()
		// either pacoloco or client has the latest version, no need to redownload it
		return nil
	default:
		// for most dbs signatures are optional, be quiet if the signature is not found
		// quiet := resp.StatusCode == http.StatusNotFound && strings.HasSuffix(url, ".db.sig")
		return fmt.Errorf("unable to download url %s, status code is %d", upstreamURL, resp.StatusCode)
	}

	if lastModified := resp.Header.Get("Last-Modified"); lastModified != "" {
		if lastModified, err := http.ParseTime(lastModified); err == nil {
			d.modificationTime = lastModified
		}
	}

	if contentLength := resp.Header.Get("Content-Length"); contentLength != "" {
		if contentLength, err := strconv.Atoi(contentLength); err == nil {
			d.contentLength = contentLength
		}
	}

	d.eventCond.L.Lock()
	d.eventMetadataReceived = true
	d.eventCond.Broadcast()
	d.eventCond.L.Unlock()

	if err := d.copyToBufferFile(resp.Body); err != nil {
		return err
	}

	if d.eventDataReceivedSize != d.contentLength {
		return fmt.Errorf("receiving file %v: Content-Length is %v while received body length is %v", upstreamURL, d.contentLength, d.eventDataReceivedSize)
	}

	if err := os.Rename(d.bufferFile.Name(), d.outputFileName); err != nil {
		return err
	}

	if !d.modificationTime.IsZero() {
		if err := os.Chtimes(d.outputFileName, time.Now(), d.modificationTime); err != nil {
			return err
		}
	}

	cacheSizeGauge.WithLabelValues(d.repoName).Add(float64(d.contentLength))
	cachePackageGauge.WithLabelValues(d.repoName).Inc()

	return nil
}

func (d *Downloader) copyToBufferFile(in io.ReadCloser) error {
	out := d.bufferFile
	buff := make([]byte, 1024*1024)

	for {
		n, err := in.Read(buff)
		if n > 0 {
			if _, err2 := out.Write(buff[:n]); err2 != nil {
				return err2
			}

			d.eventCond.L.Lock()
			d.eventDataReceivedSize += n
			d.eventCond.Broadcast()
			d.eventCond.L.Unlock()
		}

		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func (d *Downloader) waitForCompletion() error {
	d.eventCond.L.Lock()
	for !d.eventDone {
		d.eventCond.Wait()
	}
	d.eventCond.L.Unlock()

	return d.eventError
}

func getDownloadReader(f *RequestedFile) (time.Time, io.ReadSeekCloser, error) {
	d, err := getDownloader(f)
	if err != nil {
		return time.Time{}, nil, err
	}
	if d == nil {
		// use cached file
		return time.Time{}, nil, nil
	}

	// otherwise wait till metadata is received so we can check the server file modification time
	d.eventCond.L.Lock()
	for !(d.eventMetadataReceived || d.eventDone || d.eventNotModified) {
		d.eventCond.Wait()
	}
	d.eventCond.L.Unlock()

	if d.eventNotModified {
		// serve file from the local cache
		d.decrementUsage() // usage of 'd' ends here
		return time.Time{}, nil, nil
	}

	if d.eventMetadataReceived {
		modTime := d.modificationTime
		r := &DownloadReader{
			downloader: d,
		}

		return modTime, r, nil
	}

	// we are done downloading without correctly received metadata, it is an error
	// end 'd' properly
	d.decrementUsage()
	// if cache exists, use that
	if f.cachedFileExists() {
		return time.Time{}, nil, nil
	}
	return time.Time{}, nil, d.eventError
}

type DownloadReader struct {
	downloader *Downloader
	offset     int
}

func (d *DownloadReader) Close() error {
	d.downloader.decrementUsage()
	return nil
}

func (d *DownloadReader) Read(p []byte) (int, error) {
	d.downloader.eventCond.L.Lock()
	for !(d.downloader.eventDataReceivedSize > d.offset || d.downloader.eventDone) {
		d.downloader.eventCond.Wait()
	}
	d.downloader.eventCond.L.Unlock()

	if d.downloader.eventDataReceivedSize > d.offset {
		n, err := d.downloader.bufferFile.ReadAt(p, int64(d.offset))
		d.offset += n
		if err == io.EOF {
			// EOF on the bufferFile does not mean that we are done downloading
			// EOF must be sent only once the download is complete
			err = nil
		}
		return n, err
	} else if d.downloader.eventDone {
		return 0, io.EOF
	}

	return 0, fmt.Errorf("unknown read error")
}

func (d *DownloadReader) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		d.offset = int(offset)
	case io.SeekCurrent:
		d.offset += int(offset)
	case io.SeekEnd:
		// note that we can call Seek method only after the metadata is received
		d.offset = d.downloader.contentLength
	default:
		return 0, fmt.Errorf("unknown whence parameter: %v", whence)
	}

	return int64(d.offset), nil
}

// getDownloader returns a downloader that represents currently (and asynchronously) downloaded file
// if the function returns nil for downloader and error then it means no download happens and the cached file needs to be serverd.
// the caller of this function must invoke d.decreaseUsageCount() after done dealing with downloader
func getDownloader(f *RequestedFile) (*Downloader, error) {
	forceCheck := f.forceCheckAtServer()
	if f.cachedFileExists() && !forceCheck {
		return nil, nil
	}

	downloadersMutex.Lock()
	defer downloadersMutex.Unlock()

	// check in case the file was downloaded right before we've got the lock
	if f.cachedFileExists() && !forceCheck {
		return nil, nil
	}

	key := f.key()

	d, ok := downloaders[key]
	if !ok {
		bufferFile, err := os.Create(f.bufferFileName())
		if err != nil {
			return nil, err
		}

		m := &sync.Mutex{}
		cond := sync.NewCond(m)

		d = &Downloader{
			key:            key,
			urlPath:        f.urlPath(),
			outputFileName: f.cachedFilePath,
			bufferFile:     bufferFile,
			repoName:       f.repoName,
			repo:           config.Repos[f.repoName],
			eventCond:      cond,
		}
		d.usageCount.Add(1) // one downloader is in use by the caller
		downloaders[key] = d

		d.usageCount.Add(1) // one downloader is in use by download() function
		// start downloading the data asynchronously
		go func() {
			err := d.download()
			if err != nil {
				log.Println(err)
			}

			d.eventCond.L.Lock()
			d.eventDone = true
			d.eventError = err
			d.eventCond.Broadcast()
			d.eventCond.L.Unlock()

			d.decrementUsage()
		}()
	} else {
		d.usageCount.Add(1)
	}

	return d, nil
}

type RequestedFile struct {
	repoName   string
	pathAtRepo string
	fileName   string

	cacheDir       string
	cachedFilePath string
}

func parseRequestURL(urlPath string) (*RequestedFile, error) {
	matches := pathRegex.FindStringSubmatch(urlPath)
	if len(matches) == 0 {
		return nil, fmt.Errorf("input url path '%v' does not match expected format", urlPath)
	}
	repoName := matches[1]
	pathAtRepo := matches[2]
	fileName := matches[3]

	cachePath := filepath.Join(config.CacheDir, "pkgs", repoName)
	cachedFilePath := filepath.Join(cachePath, fileName)

	return &RequestedFile{
		repoName:       repoName,
		pathAtRepo:     pathAtRepo,
		fileName:       fileName,
		cacheDir:       cachePath,
		cachedFilePath: cachedFilePath,
	}, nil
}

func (f *RequestedFile) getRepo() *Repo {
	return config.Repos[f.repoName]
}

// key used for downloaders map; each active Downloader is referenced by its key
func (f *RequestedFile) key() string {
	return f.repoName + f.urlPath()
}

func (f *RequestedFile) urlPath() string {
	return f.pathAtRepo + "/" + f.fileName
}

// mkCacheDir creates cache directory if one does not exist
func (f *RequestedFile) mkCacheDir() error {
	if _, err := os.Stat(f.cacheDir); os.IsNotExist(err) {
		if err := os.MkdirAll(f.cacheDir, os.ModePerm); err != nil {
			return err
		}
	}
	return nil
}

func (f *RequestedFile) cachedFileExists() bool {
	if _, err := os.Stat(f.cachedFilePath); err == nil {
		return true
	}
	return false
}

// temporary filename used to temporary contain the downloaded data
func (f *RequestedFile) bufferFileName() string {
	return path.Join(f.cacheDir, "."+f.fileName)
}

func (f *RequestedFile) forceCheckAtServer() bool {
	return forceCheckAtServer(f.fileName)
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
