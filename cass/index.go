package cass

import "context"

// Index defines the interface for session storage and search.
type Index interface {
	// BatchIndex adds or updates sessions atomically.
	BatchIndex(ctx context.Context, sessions []Session) error

	// Search executes a query and returns matching results.
	Search(ctx context.Context, req SearchRequest) (*SearchResult, error)

	// Delete removes sessions matching the filter.
	Delete(ctx context.Context, filter DeleteFilter) error

	// Close releases underlying resources.
	Close() error
}
