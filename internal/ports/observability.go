package ports

import "logagent/internal/domain"

// AgentObserver is the framework-neutral, non-blocking observation boundary.
// Implementations must not expose errors to the investigation path.
type AgentObserver interface {
	Record(domain.AgentEvent)
}
