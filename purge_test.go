package main

import (
	"io/ioutil"
	"log"
	"os"
	"path"
	"testing"
	"time"
)

func TestPurge(t *testing.T) {
	purgeFilesAfter := 3600 * 24 * 30 // purge files if they are not accessed for 30 days

	testPacolocoDir, err := ioutil.TempDir(os.TempDir(), "*-pacoloco-repo")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(testPacolocoDir) // clean up

	testRepo := path.Join(testPacolocoDir, "pkgs", "purgerepo")
	if err := os.MkdirAll(testRepo, os.ModePerm); err != nil {
		log.Fatal(err)
	}

	fileToRemove := path.Join(testRepo, "toremove")
	fileToStay := path.Join(testRepo, "tostay")
	fileOutsideRepo := path.Join(testPacolocoDir, "outsiderepo")

	thresholdTime := time.Now().Add(time.Duration(-purgeFilesAfter) * time.Second)

	os.Create(fileToRemove)
	os.Chtimes(fileToRemove, thresholdTime.Add(-time.Hour), thresholdTime.Add(-time.Hour))

	os.Create(fileToStay)
	os.Chtimes(fileToStay, thresholdTime.Add(time.Hour), thresholdTime.Add(-time.Hour))

	os.Create(fileOutsideRepo)
	os.Chtimes(fileToRemove, thresholdTime.Add(-time.Hour), thresholdTime.Add(-time.Hour))

	purgeStaleFiles(testPacolocoDir, purgeFilesAfter)

	if _, err := os.Stat(fileToRemove); !os.IsNotExist(err) {
		t.Fail()
	}

	if _, err := os.Stat(fileToStay); err != nil {
		t.Fail()
	}
	// files outside of the pkgs cache should not be touched
	if _, err := os.Stat(fileOutsideRepo); err != nil {
		t.Fail()
	}
}

func TestPurgeSeconds(t *testing.T) {
	r := Repo{}
	if r.purgeSeconds() != config.PurgeFilesAfter {
		t.Fail()
	}

	n := 99
	if r.PurgeFilesAfter = &n; r.purgeSeconds() != n {
		t.Fail()
	}
}
