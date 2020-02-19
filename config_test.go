package main

import "testing"

func TestLoadConfig(t *testing.T) {
	_ = readConfig("config.test.yaml")
}
