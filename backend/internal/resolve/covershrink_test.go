package resolve

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
)

// The pictures below are kept as small as the thresholds allow: the largest is
// a megapixel and a half, decoded one at a time.

// paintedCover is a cover-shaped picture with something in it — bands of
// colour and a grain — so that it compresses like art rather than like a blank
// page. The grain is a fixed sequence: the same picture on every run.
func paintedCover(width int, height int, grain uint8) *image.RGBA {
	picture := image.NewRGBA(image.Rect(0, 0, width, height))
	state := uint32(2463534242)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			state ^= state << 13
			state ^= state >> 17
			state ^= state << 5
			noise := uint8(state) % (grain + 1)
			picture.SetRGBA(x, y, color.RGBA{
				R: uint8(x*255/width) - noise/2,
				G: uint8(y*255/height) - noise/2,
				B: 90 + noise,
				A: 255,
			})
		}
	}
	return picture
}

func asJPEG(t *testing.T, picture image.Image, quality int) []byte {
	t.Helper()
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, picture, &jpeg.Options{Quality: quality}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buffer.Bytes()
}

func asPNG(t *testing.T, picture image.Image) []byte {
	t.Helper()
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, picture); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buffer.Bytes()
}

func sizeOf(t *testing.T, body []byte) (width int, height int, format string) {
	t.Helper()
	config, format, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("read the picture's size: %v", err)
	}
	return config.Width, config.Height, format
}

// A cover far bigger than a card needs is stored as a JPEG no wider than
// coverMaxWidth, in the shape it came in. One that is already light is left
// exactly as the site sent it.
func TestShrinkCoverBringsAnOversizedCoverDownToCardSize(t *testing.T) {
	cases := []struct {
		name       string
		body       []byte
		wantWidth  int
		wantHeight int
	}{
		{"a wide JPEG", asJPEG(t, paintedCover(1280, 1820, 20), 95), 640, 910},
		{"a wide PNG", asPNG(t, paintedCover(900, 1350, 40)), 640, 960},
		{"a landscape cover", asJPEG(t, paintedCover(1500, 900, 20), 95), 640, 384},
		// No wider than a card needs, but heavy: re-encoded at its own size.
		{"a narrow PNG that weighs a lot", asPNG(t, paintedCover(500, 750, 60)), 500, 750},
	}
	for _, testCase := range cases {
		smaller, ext, ok := ShrinkCover(testCase.body)
		if !ok {
			t.Errorf("%s (%d bytes) was kept as it came", testCase.name, len(testCase.body))
			continue
		}
		width, height, format := sizeOf(t, smaller)
		if ext != ".jpg" || format != "jpeg" {
			t.Errorf("%s: stored as %s (%s), want a JPEG", testCase.name, ext, format)
		}
		if width != testCase.wantWidth || height != testCase.wantHeight {
			t.Errorf("%s: %dx%d, want %dx%d", testCase.name, width, height, testCase.wantWidth, testCase.wantHeight)
		}
		if len(smaller) > len(testCase.body)*9/10 {
			t.Errorf("%s: %d bytes from %d is no saving", testCase.name, len(smaller), len(testCase.body))
		}
		// Its own output is not shrunk again: running the tool twice, or a
		// cover downloaded twice, must not degrade it generation by generation.
		if _, _, again := ShrinkCover(smaller); again {
			t.Errorf("%s: the smaller copy was shrunk a second time", testCase.name)
		}
	}
}

func TestShrinkCoverLeavesAloneWhatItShould(t *testing.T) {
	light := asJPEG(t, paintedCover(400, 600, 10), 80)
	if len(light) > coverLightBytes {
		t.Fatalf("fixture: the light cover is %d bytes", len(light))
	}
	rotated := withEXIFOrientation(t, asJPEG(t, paintedCover(1280, 1820, 20), 95), 6)
	upright := withEXIFOrientation(t, asJPEG(t, paintedCover(1280, 1820, 20), 95), 1)

	kept := map[string][]byte{
		"a cover already light":             light,
		"a WebP, which is compact as it is": []byte("RIFF\x24\x00\x00\x00WEBPVP8 \x18\x00\x00\x000\x01\x00\x9d\x01\x2a\x01\x00\x01\x00"),
		"a GIF, which may be animated":      []byte("GIF89a\x00\x05\x00\x08\x80\x00\x00\x00\x00\x00\xff\xff\xff,\x00\x00\x00\x00\x01\x00\x01\x00\x00\x02\x02D\x01\x00;"),
		"something that is not a picture":   []byte("<html><body>Just a moment...</body></html>"),
		"nothing at all":                    nil,
		// Wide, but a few kilobytes as it is: a JPEG of it would weigh more.
		"a wide picture that is tiny already":   asPNG(t, paintedCover(900, 1350, 0)),
		"a picture too large to decode":         pngClaiming(t, 6000, 5000),
		"a JPEG its EXIF tag turns on its side": rotated,
	}
	for name, body := range kept {
		if _, _, ok := ShrinkCover(body); ok {
			t.Errorf("%s was re-encoded", name)
		}
	}

	// An EXIF block that says "upright" is no reason to keep seven megabytes.
	if _, _, ok := ShrinkCover(upright); !ok {
		t.Error("a JPEG whose EXIF orientation is upright was kept as it came")
	}
}

// Transparency is flattened onto white, not onto the black a JPEG encoder
// makes of an alpha channel it ignores.
func TestShrinkCoverFlattensTransparencyOntoWhite(t *testing.T) {
	logo := image.NewNRGBA(image.Rect(0, 0, 900, 900))
	for y := 300; y < 600; y++ {
		for x := 300; x < 600; x++ {
			logo.SetNRGBA(x, y, color.NRGBA{R: 200, G: 30, B: 30, A: 255})
		}
	}
	// A grain in the alpha-free middle so the PNG is not trivially small.
	painted := paintedCover(900, 900, 60)
	for y := 0; y < 900; y++ {
		for x := 0; x < 900; x++ {
			if x >= 300 && x < 600 && y >= 300 && y < 600 {
				logo.SetNRGBA(x, y, color.NRGBA{R: painted.RGBAAt(x, y).R, G: 30, B: 30, A: 255})
			}
		}
	}

	smaller, _, ok := ShrinkCover(asPNG(t, logo))
	if !ok {
		t.Fatal("the picture was kept as it came")
	}
	flattened, err := jpeg.Decode(bytes.NewReader(smaller))
	if err != nil {
		t.Fatalf("decode the smaller copy: %v", err)
	}
	r, g, b, _ := flattened.At(20, 20).RGBA()
	if r>>8 < 245 || g>>8 < 245 || b>>8 < 245 {
		t.Fatalf("the transparent corner came out as rgb(%d,%d,%d), want white", r>>8, g>>8, b>>8)
	}
}

// Area averaging: each destination pixel is the mean of the source area under
// it, partial pixels counted by the part covered.
func TestResampleAreaAveragesTheSourceUnderEachPixel(t *testing.T) {
	// Halving: every destination pixel is the mean of a 2x2 block.
	source := image.NewRGBA(image.Rect(0, 0, 4, 2))
	for x, value := range []uint8{0, 100, 40, 60} {
		for y := 0; y < 2; y++ {
			source.SetRGBA(x, y, color.RGBA{R: value, G: value, B: value, A: 255})
		}
	}
	halved := resampleArea(source, 2, 1)
	if got := []uint8{halved.RGBAAt(0, 0).R, halved.RGBAAt(1, 0).R}; got[0] != 50 || got[1] != 50 {
		t.Fatalf("halving [0 100 40 60] gave %v, want [50 50]", got)
	}

	// Three pixels onto two: the middle one is shared half and half.
	spans := areaWeights(3, 2)
	want := [][]float32{{2.0 / 3, 1.0 / 3}, {1.0 / 3, 2.0 / 3}}
	for index, span := range spans {
		if span.start != index || len(span.weights) != 2 {
			t.Fatalf("span %d = %+v, want two pixels from %d", index, span, index)
		}
		var total float32
		for position, weight := range span.weights {
			total += weight
			if diff := weight - want[index][position]; diff > 1e-5 || diff < -1e-5 {
				t.Fatalf("span %d weights %v, want %v", index, span.weights, want[index])
			}
		}
		if total < 0.9999 || total > 1.0001 {
			t.Fatalf("span %d weights add up to %f", index, total)
		}
	}

	// Whatever the ratio, a flat colour stays that colour: the weights of
	// every span add up to one.
	flat := image.NewRGBA(image.Rect(0, 0, 37, 53))
	for index := 0; index < len(flat.Pix); index += 4 {
		flat.Pix[index], flat.Pix[index+1], flat.Pix[index+2], flat.Pix[index+3] = 77, 133, 201, 255
	}
	scaled := resampleArea(flat, 11, 16)
	for y := 0; y < 16; y++ {
		for x := 0; x < 11; x++ {
			if got := scaled.RGBAAt(x, y); got != (color.RGBA{R: 77, G: 133, B: 201, A: 255}) {
				t.Fatalf("pixel (%d,%d) of a flat picture came out %v", x, y, got)
			}
		}
	}
}

// The resolver stores the smaller copy, under the name of what it stored, and
// finds it again without another resize.
func TestStoreLocallyKeepsTheSmallerCopy(t *testing.T) {
	big := asPNG(t, paintedCover(900, 1350, 40))
	var hits atomic.Int64
	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(big)
	}))
	defer imageServer.Close()

	resolver := newLocalCoverResolver(t, connectors.NewRegistry())
	coverURL := imageServer.URL + "/huge.png"

	name, ok := resolver.storeLocally(context.Background(), coverURL)
	if !ok || !strings.HasSuffix(name, ".jpg") {
		t.Fatalf("stored as %q (ok=%v), want a .jpg", name, ok)
	}
	stored, err := os.ReadFile(filepath.Join(resolver.dir, name))
	if err != nil {
		t.Fatalf("read the stored cover: %v", err)
	}
	if width, height, format := sizeOf(t, stored); width != coverMaxWidth || height != 960 || format != "jpeg" {
		t.Fatalf("stored a %dx%d %s, want a %dx960 jpeg", width, height, format, coverMaxWidth)
	}
	if len(stored) >= len(big) {
		t.Fatalf("stored %d bytes of a %d byte download", len(stored), len(big))
	}
	entries, err := os.ReadDir(resolver.dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("the cover directory holds %d files (%v), want the one cover", len(entries), err)
	}

	// The same cover asked for again is the file already there.
	again, ok := resolver.storeLocally(context.Background(), coverURL)
	if !ok || again != name {
		t.Fatalf("the second store returned %q (ok=%v), want %q", again, ok, name)
	}
	if entries, _ := os.ReadDir(resolver.dir); len(entries) != 1 {
		t.Fatalf("the second store left %d files", len(entries))
	}
}

// withEXIFOrientation inserts an EXIF block carrying one tag, the
// orientation, right after a JPEG's start-of-image marker.
func withEXIFOrientation(t *testing.T, picture []byte, orientation uint16) []byte {
	t.Helper()
	if len(picture) < 2 || picture[0] != 0xFF || picture[1] != 0xD8 {
		t.Fatal("fixture: not a JPEG")
	}
	var tiff bytes.Buffer
	tiff.WriteString("MM\x00\x2a")                             // big-endian TIFF
	_ = binary.Write(&tiff, binary.BigEndian, uint32(8))       // first directory follows the header
	_ = binary.Write(&tiff, binary.BigEndian, uint16(1))       // one entry
	_ = binary.Write(&tiff, binary.BigEndian, uint16(0x0112))  // orientation
	_ = binary.Write(&tiff, binary.BigEndian, uint16(3))       // SHORT
	_ = binary.Write(&tiff, binary.BigEndian, uint32(1))       // one value
	_ = binary.Write(&tiff, binary.BigEndian, orientation)     // the value...
	_ = binary.Write(&tiff, binary.BigEndian, uint16(0))       // ...padded to four bytes
	_ = binary.Write(&tiff, binary.BigEndian, uint32(0))       // no further directory
	payload := append([]byte("Exif\x00\x00"), tiff.Bytes()...) //nolint:gocritic // a new slice is the point

	var out bytes.Buffer
	out.Write(picture[:2])
	out.Write([]byte{0xFF, 0xE1})
	_ = binary.Write(&out, binary.BigEndian, uint16(len(payload)+2))
	out.Write(payload)
	out.Write(picture[2:])
	return out.Bytes()
}

// pngClaiming is a PNG header and nothing else: enough for the file to state
// its dimensions, which is all that is read of a picture too large to decode.
func pngClaiming(t *testing.T, width uint32, height uint32) []byte {
	t.Helper()
	var header bytes.Buffer
	_ = binary.Write(&header, binary.BigEndian, width)
	_ = binary.Write(&header, binary.BigEndian, height)
	header.Write([]byte{8, 2, 0, 0, 0}) // 8-bit RGB, no interlace
	chunk := append([]byte("IHDR"), header.Bytes()...)

	var out bytes.Buffer
	out.WriteString("\x89PNG\r\n\x1a\n")
	_ = binary.Write(&out, binary.BigEndian, uint32(header.Len()))
	out.Write(chunk)
	_ = binary.Write(&out, binary.BigEndian, crc32.ChecksumIEEE(chunk))
	if width, height, _ := sizeOf(t, out.Bytes()); width != 6000 || height != 5000 {
		t.Fatalf("fixture: the header reads as %dx%d", width, height)
	}
	return out.Bytes()
}
