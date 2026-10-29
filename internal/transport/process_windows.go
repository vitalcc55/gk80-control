package transport

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func configureCoreProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
}
