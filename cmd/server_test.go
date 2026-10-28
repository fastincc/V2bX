package cmd

import "testing"

func TestRun(t *testing.T) {
	Run()
}

func TestServerStartupFailureReturnsError(t *testing.T) {
	old := config
	defer func() { config = old }()
	config = t.TempDir() + "/missing.json"
	if err := serverHandle(nil, nil); err == nil {
		t.Fatal("startup failure returned success")
	}
}
