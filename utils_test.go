package main

import (
	"os"
	"path"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFileExistsWithExistingFile(t *testing.T) {
	temp := t.TempDir()
	filePath := path.Join(temp, "testfile")
	require.NoError(t, os.WriteFile(filePath, []byte("content"), 0o644))

	exists, err := fileExists(filePath)
	require.NoError(t, err)
	require.True(t, exists)
}

func TestFileExistsWithNonExistingFile(t *testing.T) {
	exists, err := fileExists("/nonexistent/path/to/file")
	require.NoError(t, err)
	require.False(t, exists)
}

func TestFileExistsWithDirectory(t *testing.T) {
	temp := t.TempDir()
	exists, err := fileExists(temp)
	require.NoError(t, err)
	require.True(t, exists)
}
