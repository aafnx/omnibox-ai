//go:build !windows

package host

import (
	"os/exec"
	"syscall"
)

// tree is the engine's process group; stopping signals the whole group.
type tree struct{}

func newTree() (*tree, error) { return &tree{}, nil }

func (t *tree) prepare(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

func (t *tree) started(*exec.Cmd) error { return nil }

func (t *tree) stop(cmd *exec.Cmd) { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }

func (t *tree) kill(cmd *exec.Cmd) { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }

func (t *tree) close() {}
