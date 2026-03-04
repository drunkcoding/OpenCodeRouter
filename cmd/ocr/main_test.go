package main

import "testing"

func TestNewRootCmd_DebugFlagRegistered(t *testing.T) {
	t.Parallel()

	cmd := newRootCmd()
	flag := cmd.Flags().Lookup("debug")
	if flag == nil {
		t.Fatal("expected --debug flag to be registered")
	}
	if flag.DefValue != "false" {
		t.Fatalf("expected --debug default false, got %q", flag.DefValue)
	}
}

func TestNewRootCmd_LogFileFlagRegistered(t *testing.T) {
	t.Parallel()

	cmd := newRootCmd()
	flag := cmd.Flags().Lookup("log-file")
	if flag == nil {
		t.Fatal("expected --log-file flag to be registered")
	}
	if flag.DefValue != "" {
		t.Fatalf("expected --log-file default empty string, got %q", flag.DefValue)
	}
}
