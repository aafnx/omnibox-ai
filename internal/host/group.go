package host

import (
	"fmt"
	"os/exec"
	"sync"
)

// Group keeps a helper process of the service and everything it starts together, as the engines'
// trees are kept: a Job Object on Windows (closed with the service, so nothing outlives it), a
// process group on Linux.
type Group struct {
	t    *tree
	cmd  *exec.Cmd
	once sync.Once
}

// StartGroup starts cmd inside a new group.
func StartGroup(cmd *exec.Cmd) (*Group, error) {
	t, err := newTree()
	if err != nil {
		return nil, fmt.Errorf("Не удалось объединить процессы: %v", err)
	}
	t.prepare(cmd)
	if err := cmd.Start(); err != nil {
		t.close()
		return nil, err
	}
	if err := t.started(cmd); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.close()
		return nil, fmt.Errorf("Не удалось объединить процессы: %v", err)
	}
	return &Group{t: t, cmd: cmd}, nil
}

// Kill ends the whole group at once.
func (g *Group) Kill() {
	g.once.Do(func() {
		g.t.kill(g.cmd)
		g.t.close()
	})
}
