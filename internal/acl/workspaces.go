package acl

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// userDirMode is the mode a per-user directory under workspace.users_root is
// created with: nobody but the daemon's own account looks inside.
const userDirMode = 0o700

type workspaces struct {
	cfg config.Config
	st  ports.Store
}

var _ ports.Workspaces = (*workspaces)(nil)

// NewWorkspaces decides where each user may work (spec-p4-b1 3.4) from the
// [workspace] table of cfg, telling the bot owner apart through the roles in
// st.
func NewWorkspaces(cfg config.Config, st ports.Store) ports.Workspaces {
	return &workspaces{cfg: cfg, st: st}
}

// Roots answers, first match wins: the bot owner works in workspace.roots; a
// user with a [[workspace.users]] entry in its roots; while users_root is set,
// everyone else in <users_root>/<config.UserDirName>, created when missing;
// and otherwise in workspace.roots, shared as in P1–P3.
func (w *workspaces) Roots(ctx context.Context, ep fsp.EndpointID, senderID string) ([]string, error) {
	if IsBotOwner(ctx, w.st, ep, senderID) {
		return w.shared()
	}
	for _, u := range w.cfg.Workspace.Users {
		if fsp.Platform(u.Platform) != ep.Platform || u.ID != senderID {
			continue
		}
		roots, err := u.NormalizedRoots()
		if err != nil {
			return nil, fmt.Errorf("workspaces: %w", err)
		}
		if len(roots) == 0 {
			return nil, fmt.Errorf("workspaces: workspace.users %s %q has no roots", u.Platform, u.ID)
		}
		return roots, nil
	}
	usersRoot, err := w.cfg.NormalizedUsersRoot()
	if err != nil {
		return nil, fmt.Errorf("workspaces: %w", err)
	}
	if usersRoot == "" {
		return w.shared()
	}
	dir := filepath.Join(usersRoot, config.UserDirName(ep.Platform, senderID))
	if err := os.MkdirAll(dir, userDirMode); err != nil {
		return nil, fmt.Errorf("workspaces: create %s: %w", dir, err)
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, fmt.Errorf("workspaces: %w", err)
	}
	return []string{resolved}, nil
}

// shared is workspace.roots, the roots of the owner and, without users_root,
// of everyone.
func (w *workspaces) shared() ([]string, error) {
	roots, err := w.cfg.NormalizedRoots()
	if err != nil {
		return nil, fmt.Errorf("workspaces: %w", err)
	}
	if len(roots) == 0 {
		return nil, errors.New("workspaces: workspace.roots is empty")
	}
	return roots, nil
}
