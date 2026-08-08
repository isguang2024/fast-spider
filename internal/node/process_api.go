package node

import "os/exec"

// ConfigureProcessTree applies the Node's platform-specific process-tree
// policy to a child command.
func ConfigureProcessTree(cmd *exec.Cmd) { configureProcessTree(cmd) }

// KillProcessTree terminates a child command and its descendants using the
// Node's platform-specific process-tree policy.
func KillProcessTree(cmd *exec.Cmd) error { return killProcessTree(cmd) }
