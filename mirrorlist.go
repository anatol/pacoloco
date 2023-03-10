package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

func updateMirrorlists() {
	for name, repo := range config.Repos {
		err := checkAndUpdateMirrorlistRepo(name, repo)
		if err != nil {
			log.Fatal(err)
		}
	}
}

// wrapper that maps errors to a consistent format.
func checkAndUpdateMirrorlistRepo(repoName string, repo *Repo) error {
	err := tryCheckAndUpdateMirrorlistRepo(repoName, repo)
	if err != nil {
		err = fmt.Errorf("error while updating %v repo mirrorlist: %w", repoName, err)
	}
	return err
}

func tryCheckAndUpdateMirrorlistRepo(repoName string, repo *Repo) error {
	if repo.Mirrorlist == "" {
		return nil
	}

	repo.timestampsMutex.Lock()
	defer repo.timestampsMutex.Unlock()

	// if we checked recently then skip it
	if time.Since(repo.LastMirrorlistCheck) < 5*time.Second {
		return nil
	}
	repo.LastMirrorlistCheck = time.Now()

	fileInfo, err := os.Stat(repo.Mirrorlist)
	if err != nil {
		return err
	}

	fileModTime := fileInfo.ModTime()
	// if it hasn't been updated then skip it
	if fileModTime == repo.LastModificationTime {
		return nil
	}
	repo.LastModificationTime = fileModTime

	// it's out of date so now update the URLs from the mirrorlist.
	// open readonly, it won't change modification time
	file, err := os.Open(repo.Mirrorlist)
	if err != nil {
		return err
	}
	defer file.Close()

	repo.urlsMutex.Lock()
	defer repo.urlsMutex.Unlock()

	// initialize the urls collection
	repo.URLs = make([]string, 0)
	scanner := bufio.NewScanner(file)
	// resize scanner's capacity if lines are longer than 64K.
	for scanner.Scan() {
		matches := mirrorlistRegex.FindStringSubmatch(scanner.Text())
		if len(matches) > 0 { // skip invalid lines
			url := matches[1]
			if !strings.Contains(url, "$") {
				repo.URLs = append(repo.URLs, url)
			} else {
				// this can be a regex error or otherwise a very peculiar url
				log.Printf("warning: %v url in repo %v contains suspicious characters, skipping it", url, repoName)
			}
		}
	}

	if len(repo.URLs) == 0 {
		return fmt.Errorf("mirrorlist for repo %v is either empty or isn't a mirrorlist file", repoName)
	}

	return scanner.Err()
}
