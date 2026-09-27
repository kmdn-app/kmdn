package gitmirror

import (
	"context"
	"errors"
	"os/exec"
)

// IsAncestor checks exact commit reachability, not a commit-message match.
func (m *Mirror) IsAncestor(ctx context.Context, ancestor, head string) (bool, error) {
	_, err := m.Git.run(ctx, m.Path, nil, nil, "merge-base", "--is-ancestor", ancestor, head)
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return err == nil, err
}
