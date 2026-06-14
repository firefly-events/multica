package hive

import "context"

// WorkspaceAuthorizer checks whether the caller in ctx may access workspaceID.
// Hive handlers inherit the outer auth middleware; this interface is available
// for sub-handlers that need explicit workspace-level checks.
type WorkspaceAuthorizer interface {
	AuthorizeWorkspace(ctx context.Context, workspaceID string) error
}
