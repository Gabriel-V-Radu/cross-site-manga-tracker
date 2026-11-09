package repository

// The external test package pins the query plans of two statements. These
// hand it the exact text the repository runs, so a plan test cannot drift
// from the query it is about.
const (
	// PollingTrackersQueryForTest is listPollingTrackers' statement.
	PollingTrackersQueryForTest = pollingTrackersQuery
	// MetadataBackfillLinksQueryForTest is ListMetadataBackfillLinks'.
	MetadataBackfillLinksQueryForTest = metadataBackfillLinksQuery
)
