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
	err = os.MkdirAll(testRepo, os.ModePerm)
	if err != nil {
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

	_, err = os.Stat(fileToRemove)
	if !os.IsNotExist(err) {
		t.Fail()
	}

	_, err = os.Stat(fileToStay)
	if err != nil {
		t.Fail()
	}

	_, err = os.Stat(fileOutsideRepo) // files outside of the pkgs cache should not be touched
	if err != nil {
		t.Fail()
	}
}
