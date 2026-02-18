package handlers

import "testing"

func TestExtractMangaFireMangaURL(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		wantURL string
		wantOK  bool
	}{
		{
			name:    "accepts valid mangafire manga URL",
			query:   "https://mangafire.to/manga/one-piecee.dkw",
			wantURL: "https://mangafire.to/manga/one-piecee.dkw",
			wantOK:  true,
		},
		{
			name:    "strips query and fragment",
			query:   "https://www.mangafire.to/manga/one-piecee.dkw?x=1#top",
			wantURL: "https://www.mangafire.to/manga/one-piecee.dkw",
			wantOK:  true,
		},
		{
			name:   "rejects title text",
			query:  "One Piece",
			wantOK: false,
		},
		{
			name:   "rejects embedded URL in free text",
			query:  "check this https://mangafire.to/manga/one-piecee.dkw",
			wantOK: false,
		},
		{
			name:   "rejects non manga path",
			query:  "https://mangafire.to/read/one-piecee.dkw/en/chapter-1",
			wantOK: false,
		},
		{
			name:   "rejects other domains",
			query:  "https://example.com/manga/one-piecee.dkw",
			wantOK: false,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			gotURL, gotOK := extractMangaFireMangaURL(testCase.query)
			if gotOK != testCase.wantOK {
				t.Fatalf("expected ok=%v, got %v", testCase.wantOK, gotOK)
			}
			if gotURL != testCase.wantURL {
				t.Fatalf("expected url %q, got %q", testCase.wantURL, gotURL)
			}
		})
	}
}
