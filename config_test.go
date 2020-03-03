package main

import "testing"

func TestLoadConfig(t *testing.T) {
	config := readConfig("config.test.yaml")
	if config.PurgeFilesAfter != 2592000 {
		t.Fail()
	}
}
