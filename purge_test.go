package main

import (
	"os"
	"path"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestPurge(t *testing.T) {
	purgeFilesAfter := 3600 * 24 * 30 // purge files if they are not accessed for 30 days

	testPacolocoDir, err := os.MkdirTemp(os.TempDir(), "*-pacoloco-repo")
	require.NoError(t, err)
	defer os.RemoveAll(testPacolocoDir) // clean up

	testRepo := path.Join(testPacolocoDir, "pkgs", "purgerepo")

	require.NoError(t, os.MkdirAll(testRepo, os.ModePerm))

	cachePackageNum, err := cachePackageGauge.GetMetricWithLabelValues("purgerepo")
	require.NoError(t, err)
	cachePackageSize, err := cacheSizeGauge.GetMetricWithLabelValues("purgerepo")
	require.NoError(t, err)

	fileToRemove := path.Join(testRepo, "toremove")
	fileToStay := path.Join(testRepo, "tostay")
	fileToBePurgedLater := path.Join(testRepo, "tobepurgedlater")
	fileOutsideRepo := path.Join(testPacolocoDir, "outsiderepo")

	thresholdTime := time.Now().Add(time.Duration(-purgeFilesAfter) * time.Second)

	os.Create(fileToRemove)
	pkgFileContent := "delete me"
	require.NoError(t, os.WriteFile(fileToRemove, []byte(pkgFileContent), os.ModePerm))
	os.Chtimes(fileToRemove, thresholdTime.Add(-time.Hour), thresholdTime.Add(-time.Hour))

	os.Create(fileToStay)
	pkgFileContent = "leave me"
	require.NoError(t, os.WriteFile(fileToStay, []byte(pkgFileContent), os.ModePerm))
	infoToStay, err := os.Stat(fileToStay)
	require.NoError(t, err)
	os.Chtimes(fileToStay, thresholdTime.Add(time.Hour), thresholdTime.Add(-time.Hour))

	os.Create(fileToBePurgedLater)
	pkgFileContent = "leave me for now"
	require.NoError(t, os.WriteFile(fileToBePurgedLater, []byte(pkgFileContent), os.ModePerm))
	infoToBePurgedLater, err := os.Stat(fileToBePurgedLater)
	require.NoError(t, err)
	os.Chtimes(fileToBePurgedLater, thresholdTime.Add(time.Hour), thresholdTime.Add(-time.Hour))

	os.Create(fileOutsideRepo)
	pkgFileContent = "don't touch me"
	require.NoError(t, os.WriteFile(fileOutsideRepo, []byte(pkgFileContent), os.ModePerm))
	os.Chtimes(fileOutsideRepo, thresholdTime.Add(-time.Hour), thresholdTime.Add(-time.Hour))

	expectedPackageNum := float64(2)
	expectedSize := float64(infoToStay.Size() + infoToBePurgedLater.Size())

	purgeStaleFiles(testPacolocoDir, purgeFilesAfter, "purgerepo")

	_, err = os.Stat(fileToRemove)
	require.ErrorIs(t, err, os.ErrNotExist)

	_, err = os.Stat(fileToStay)
	require.NoError(t, err)
	_, err = os.Stat(fileToBePurgedLater)
	require.NoError(t, err)
	// files outside of the pkgs cache should not be touched
	_, err = os.Stat(fileOutsideRepo)
	require.NoError(t, err)

	actualPackageNum := testutil.ToFloat64(cachePackageNum)
	actualSize := testutil.ToFloat64(cachePackageSize)
	require.Equal(t, expectedPackageNum, actualPackageNum)
	require.Equal(t, expectedSize, actualSize)

	os.Chtimes(fileToBePurgedLater, thresholdTime.Add(-time.Hour), thresholdTime.Add(-time.Hour))

	expectedPackageNum = testutil.ToFloat64(cachePackageNum) - 1
	expectedSize = testutil.ToFloat64(cachePackageSize) - float64(infoToBePurgedLater.Size())

	purgeStaleFiles(testPacolocoDir, purgeFilesAfter, "purgerepo")

	_, err = os.Stat(fileToStay)
	require.NoError(t, err)
	_, err = os.Stat(fileToBePurgedLater)
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(fileOutsideRepo)
	require.NoError(t, err)

	actualPackageNum = testutil.ToFloat64(cachePackageNum)
	actualSize = testutil.ToFloat64(cachePackageSize)
	require.Equal(t, expectedPackageNum, actualPackageNum)
	require.Equal(t, expectedSize, actualSize)
}
