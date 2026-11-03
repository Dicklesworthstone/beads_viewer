//go:build !windows

package hooks

import (
	"reflect"
	"runtime"
	"testing"
)

func TestGetShellCommand_Unix(t *testing.T) {
	shell, args := shellInvocation(runtime.GOOS, "echo approved")
	if shell != "sh" || !reflect.DeepEqual(args, []string{"-c", "echo approved"}) {
		t.Fatalf("unexpected Unix shell invocation: %q %q", shell, args)
	}
}
