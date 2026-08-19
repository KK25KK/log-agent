package application

import (
	"context"

	"logagent/internal/domain"
)

// runJobContextKey is deliberately owned by the application layer. Adapters
// cannot manufacture a claimed job, and the checkpoint executor therefore
// only accepts calls made from a lease-fenced Worker run.
type runJobContextKey struct{}

func withRunJob(ctx context.Context, job domain.Job) context.Context {
	return context.WithValue(ctx, runJobContextKey{}, job)
}

func runJobFromContext(ctx context.Context) (domain.Job, bool) {
	if ctx == nil {
		return domain.Job{}, false
	}
	job, ok := ctx.Value(runJobContextKey{}).(domain.Job)
	return job, ok
}
