package handlers

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
	"time"
)

func TestRelativeTimeSpellsASingleUnitInTheSingular(t *testing.T) {
	now := time.Now().UTC()

	cases := []struct {
		name string
		when time.Time
		want string
	}{
		{name: "one hour", when: now.Add(-90 * time.Minute), want: "1 hour ago"},
		{name: "several hours", when: now.Add(-5 * time.Hour), want: "5 hours ago"},
		{name: "one day", when: now.Add(-30 * time.Hour), want: "1 day ago"},
		{name: "several days", when: now.Add(-72 * time.Hour), want: "3 days ago"},
		{name: "one month", when: now.Add(-31 * 24 * time.Hour), want: "1 month ago"},
		{name: "several months", when: now.Add(-90 * 24 * time.Hour), want: "3 months ago"},
		{name: "one year", when: now.Add(-400 * 24 * time.Hour), want: "1 year ago"},
		{name: "minutes keep their short form", when: now.Add(-5 * time.Minute), want: "5 min ago"},
		{name: "just now", when: now.Add(-10 * time.Second), want: "just now"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := relativeTime(tc.when); got != tc.want {
				t.Fatalf("relativeTime = %q, want %q", got, tc.want)
			}
		})
	}
}

// encodeJPEG builds a tiny image of the requested shape. The sizes here are
// deliberately small: this suite must never allocate real image data.
func encodeJPEG(t *testing.T, width int, height int) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	img.Set(0, 0, color.RGBA{R: 1, G: 2, B: 3, A: 255})
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, img, nil); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buffer.Bytes()
}

func encodePNG(t *testing.T, width int, height int) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buffer.Bytes()
}

func TestImageDimensionsMeasuresWhatItCanAndDeclinesTheRest(t *testing.T) {
	width, height, measured := imageDimensions(encodeJPEG(t, 20, 30))
	if !measured || width != 20 || height != 30 {
		t.Fatalf("jpeg measured=%v %dx%d, want true 20x30", measured, width, height)
	}

	if _, _, measured := imageDimensions(encodePNG(t, 12, 18)); !measured {
		t.Fatal("png must be measurable")
	}

	// webp and avif are not in the standard library. They must report
	// measured=false so the caller accepts them rather than judging a shape it
	// cannot read — most of the stored library is webp.
	if _, _, measured := imageDimensions([]byte("RIFF????WEBPVP8 ")); measured {
		t.Fatal("an unreadable format must not report a measurement")
	}
	if _, _, measured := imageDimensions(nil); measured {
		t.Fatal("empty bytes must not report a measurement")
	}
}

func TestCoverShapedRejectsThumbnailsAndKeepsRealCovers(t *testing.T) {
	// The shapes on the left are what the stored library actually contains.
	portrait := [][2]int{{960, 1440}, {720, 1030}, {690, 1000}, {618, 926}, {600, 800}, {480, 623}, {400, 580}, {275, 400}}
	for _, size := range portrait {
		if !coverShaped(size[0], size[1]) {
			t.Errorf("%dx%d is a real cover shape and must be accepted", size[0], size[1])
		}
	}

	// The square that started this: MangaHub served a 225x225 promo banner from
	// its cover endpoint, and accepting it ended the fallback chain on an image
	// that was not the cover.
	rejected := [][2]int{{225, 225}, {400, 400}, {600, 300}, {1200, 200}}
	for _, size := range rejected {
		if coverShaped(size[0], size[1]) {
			t.Errorf("%dx%d is not cover art and must be refused", size[0], size[1])
		}
	}
}
