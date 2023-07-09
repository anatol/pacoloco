package main

import (
	"log"
	"os"
	"path"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestPurge(t *testing.T) {
	purgeFilesAfter := 3600 * 24 * 30 // purge files if they are not accessed for 30 days

	testPacolocoDir, err := os.MkdirTemp(os.TempDir(), "*-pacoloco-repo")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(testPacolocoDir) // clean up

	testRepo := path.Join(testPacolocoDir, "pkgs", "purgerepo")
	if err := os.MkdirAll(testRepo, os.ModePerm); err != nil {
		log.Fatal(err)
	}

	cachePackageNum, err := cachePackageGauge.GetMetricWithLabelValues("purgerepo")
	if err != nil {
		t.Error(err)
	}
	cachePackageSize, err := cacheSizeGauge.GetMetricWithLabelValues("purgerepo")
	if err != nil {
		t.Error(err)
	}

	fileToRemove := path.Join(testRepo, "toremove")
	fileToStay := path.Join(testRepo, "tostay")
	fileToBePurgedLater := path.Join(testRepo, "tobepurgedlater")
	fileOutsideRepo := path.Join(testPacolocoDir, "outsiderepo")

	thresholdTime := time.Now().Add(time.Duration(-purgeFilesAfter) * time.Second)

	os.Create(fileToRemove)
	pkgFileContent := "delete me"
	if err := os.WriteFile(fileToRemove, []byte(pkgFileContent), os.ModePerm); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(fileToRemove, thresholdTime.Add(-time.Hour), thresholdTime.Add(-time.Hour))

	os.Create(fileToStay)
	pkgFileContent = "leave me"
	if err := os.WriteFile(fileToStay, []byte(pkgFileContent), os.ModePerm); err != nil {
		t.Fatal(err)
	}
	infoToStay, err := os.Stat(fileToStay)
	if err != nil {
		t.Fatal(err)
	}
	os.Chtimes(fileToStay, thresholdTime.Add(time.Hour), thresholdTime.Add(-time.Hour))

	os.Create(fileToBePurgedLater)
	pkgFileContent = "leave me for now"
	if err := os.WriteFile(fileToBePurgedLater, []byte(pkgFileContent), os.ModePerm); err != nil {
		t.Fatal(err)
	}
	infoToBePurgedLater, err := os.Stat(fileToBePurgedLater)
	if err != nil {
		t.Fatal(err)
	}
	os.Chtimes(fileToBePurgedLater, thresholdTime.Add(time.Hour), thresholdTime.Add(-time.Hour))

	os.Create(fileOutsideRepo)
	pkgFileContent = "don't touch me"
	if err := os.WriteFile(fileOutsideRepo, []byte(pkgFileContent), os.ModePerm); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(fileOutsideRepo, thresholdTime.Add(-time.Hour), thresholdTime.Add(-time.Hour))

	expectedPackageNum := testutil.ToFloat64(cachePackageNum) + 2
	expectedSize := testutil.ToFloat64(cachePackageSize) + float64(infoToStay.Size()+infoToBePurgedLater.Size())

	purgeStaleFiles(testPacolocoDir, purgeFilesAfter, "purgerepo")

	if _, err := os.Stat(fileToRemove); !os.IsNotExist(err) {
		t.Fail()
	}

	if _, err := os.Stat(fileToStay); err != nil {
		t.Fail()
	}
	if _, err := os.Stat(fileToBePurgedLater); err != nil {
		t.Fail()
	}
	// files outside of the pkgs cache should not be touched
	if _, err := os.Stat(fileOutsideRepo); err != nil {
		t.Fail()
	}

	actualPackageNum := testutil.ToFloat64(cachePackageNum)
	actualSize := testutil.ToFloat64(cachePackageSize)
	if expectedPackageNum != actualPackageNum {
		t.Errorf("Cache package number metric check failed: expected %v, got %v", expectedPackageNum, actualPackageNum)
	}
	if expectedSize != actualSize {
		t.Errorf("Cache size metric check failed: expected %v, got %v", expectedSize, actualSize)
	}

	os.Chtimes(fileToBePurgedLater, thresholdTime.Add(-time.Hour), thresholdTime.Add(-time.Hour))

	expectedPackageNum = testutil.ToFloat64(cachePackageNum) - 1
	expectedSize = testutil.ToFloat64(cachePackageSize) - float64(infoToBePurgedLater.Size())

	purgeStaleFiles(testPacolocoDir, purgeFilesAfter, "purgerepo")

	if _, err := os.Stat(fileToStay); err != nil {
		t.Fail()
	}
	if _, err := os.Stat(fileToBePurgedLater); !os.IsNotExist(err) {
		t.Fail()
	}
	if _, err := os.Stat(fileOutsideRepo); err != nil {
		t.Fail()
	}

	actualPackageNum = testutil.ToFloat64(cachePackageNum)
	actualSize = testutil.ToFloat64(cachePackageSize)
	if expectedPackageNum != actualPackageNum {
		t.Errorf("Cache package number metric check failed: expected %v, got %v", expectedPackageNum, actualPackageNum)
	}
	if expectedSize != actualSize {
		t.Errorf("Cache size metric check failed: expected %v, got %v", expectedSize, actualSize)
	}
}
