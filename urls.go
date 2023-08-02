package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

func (r *Repo) getUrls() []string {
	if r.Mirrorlist != "" {
		urls, err := r.getMirrorlistURLs()
		if err != nil {
			log.Printf("error getting urls from mirrorlist: %v", err.Error())
		}
		return urls
	} else if r.URL != "" {
		return []string{r.URL}
	} else {
		return r.URLs
	}
}

func parseMirrorlistURLs(file *os.File) ([]string, error) {
	var urls []string
	scanner := bufio.NewScanner(file)
	// resize scanner's capacity if lines are longer than 64K.
	for scanner.Scan() {
		matches := mirrorlistRegex.FindStringSubmatch(scanner.Text())
		if len(matches) > 0 { // skip invalid lines
			url := matches[1]
			if !strings.Contains(url, "$") {
				urls = append(urls, url)
			} else {
				// this can be a regex error or otherwise a very peculiar url
				log.Printf("warning: %v url in mirror file %v contains suspicious characters, skipping it", url, file.Name())
			}
		}
	}

	return urls, scanner.Err()
}

func (r *Repo) getMirrorlistURLs() ([]string, error) {
	if time.Since(r.LastMirrorlistCheck) < 5*time.Second {
		return r.URLs, nil
	}

	r.LastMirrorlistCheck = time.Now()

	fileInfo, err := os.Stat(r.Mirrorlist)
	if err != nil {
		return nil, err
	}

	fileModTime := fileInfo.ModTime()
	if fileModTime == r.LastModificationTime {
		return r.URLs, nil
	}

	r.LastModificationTime = fileModTime

	file, err := os.Open(r.Mirrorlist)
	if err != nil {
		return nil, err
	}

	urls, err := parseMirrorlistURLs(file)
	if err != nil {
		return nil, err
	}
	if len(urls) == 0 {
		return nil, fmt.Errorf("mirrorlist file %s contains no mirrors", r.Mirrorlist)
	}
	r.URLs = urls
	return urls, nil
}
