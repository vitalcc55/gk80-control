package transport

import (
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"
)

func TestCoreProcessUsesNoWindowFlag(t *testing.T) {
	cmd := exec.Command("unused")
	configureCoreProcess(cmd)
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.CreationFlags&windows.CREATE_NO_WINDOW == 0 || cmd.SysProcAttr.NoInheritHandles {
		t.Fatal("core process must hide its console while preserving standard handles")
	}
}
