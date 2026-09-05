package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestMain(m *testing.M) {
	// Editor dispatch tests execute a copy of this test binary under an
	// allowlisted editor name. Capture the actual argv without opening a GUI;
	// no platform shell or GUI program is required. Check the executable name
	// so an inherited capture variable cannot turn the suite into a zero-run pass.
	editorName := filepath.Base(os.Args[0])
	if capture := os.Getenv("BV_TEST_EDITOR_CAPTURE"); capture != "" && (editorName == "gedit" || editorName == "gedit.exe") {
		args, err := json.Marshal(os.Args[1:])
		if err != nil {
			panic(err)
		}
		if err := os.WriteFile(capture, args, 0o600); err != nil {
			panic(err)
		}
		os.Exit(0)
	}

	// Prevent any test from accidentally opening a browser
	os.Setenv("BV_NO_BROWSER", "1")
	os.Setenv("BV_TEST_MODE", "1")

	// Keep every test away from the developer's real ~/.config/bv: saved
	// config (tutorial progress, update-check disclosure) is disabled unless a
	// test opts back in with t.Setenv, and the XDG config dir points at a
	// throwaway directory so even opted-in tests never touch the real one.
	os.Setenv("BV_NO_SAVED_CONFIG", "1")
	isolatedConfig, err := os.MkdirTemp("", "bv-ui-test-config-*")
	if err != nil {
		panic("creating isolated XDG_CONFIG_HOME: " + err.Error())
	}
	os.Setenv("XDG_CONFIG_HOME", isolatedConfig)

	code := m.Run()
	os.RemoveAll(isolatedConfig)
	os.Exit(code)
}
