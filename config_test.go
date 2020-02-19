package main

import "testing"

func TestLoadConfig(t *testing.T) {
	_ = readConfig("pacoloco.yaml.sample")
}
