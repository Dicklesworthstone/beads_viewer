//go:build windows

package hooks

import (
	"reflect"
	"runtime"
	"testing"
)

func TestGetShellCommand_Windows(t *testing.T) {
	shell, args := shellInvocation(runtime.GOOS, "echo approved")
	if shell != "cmd" || !reflect.DeepEqual(args, []string{"/D", "/C", "echo approved"}) {
		t.Fatalf("unexpected Windows shell invocation: %q %q", shell, args)
	}
}
