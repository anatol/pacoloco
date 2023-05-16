package main

import (
	"bufio"
	"log"
	"os"
	"strings"
	"time"
)

func getCurrentURLs(r *Repo) []string {
	c := make(chan []string)
	r.urlsChan <- c
	return <-c
}

func getRepoURLs(repoName string, repo *Repo) []string {
	if repo.Mirrorlist != "" {
		urls, err := getMirrorlistURLs(repoName, repo)
		if err != nil {
			log.Printf("error getting urls from mirrorlist for repo %v: %v", repoName, err.Error())
		}
		return urls
	} else if repo.URL != "" {
		return []string{repo.URL}
	} else {
		return repo.URLs
	}
}

func parseMirrorlistURLs(repoName string, file *os.File) ([]string, error) {
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
				log.Printf("warning: %v url in repo %v contains suspicious characters, skipping it", url, repoName)
			}
		}
	}

	return urls, scanner.Err()
}

func getMirrorlistURLs(repoName string, repo *Repo) ([]string, error) {
	if time.Since(repo.LastMirrorlistCheck) < 5*time.Second {
		return repo.URLs, nil
	}

	repo.LastMirrorlistCheck = time.Now()

	fileInfo, err := os.Stat(repo.Mirrorlist)
	if err != nil {
		return nil, err
	}

	fileModTime := fileInfo.ModTime()
	if fileModTime == repo.LastModificationTime {
		return repo.URLs, nil
	}

	repo.LastModificationTime = fileModTime

	file, err := os.Open(repo.Mirrorlist)
	if err != nil {
		return nil, err
	}

	urls, err := parseMirrorlistURLs(repoName, file)
	if err == nil {
		repo.URLs = urls
	}
	return urls, err
}
