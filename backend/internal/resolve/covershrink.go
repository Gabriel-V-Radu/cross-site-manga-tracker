package resolve

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	_ "image/png" // registers the PNG decoder with image.Decode
	"sync"
)

// A stored cover is drawn 280 CSS pixels wide on a desktop grid and 290 in a
// phone's single column (measured on the dashboard). The sites serve whatever
// the uploader had: the Pi's cover directory held 92 MB for a few hundred
// series, single JPEGs and PNGs of five to seven megabytes among them, each
// sent whole to draw a thumbnail. A cover wider or heavier than the limits
// below is stored as a JPEG no wider than coverMaxWidth instead.
const (
	// coverMaxWidth is the widest a stored cover is kept: the card's width on
	// a phone at twice the pixel density and a little over, which is where a
	// narrower copy starts to look soft. Wider than that only weighs more.
	coverMaxWidth = 640
	// coverLightBytes is the size under which a cover no wider than
	// coverMaxWidth is left exactly as the site sent it.
	coverLightBytes = 150 << 10
	// coverJPEGQuality is the quality the smaller copy is encoded at: where
	// cover art stops showing the compression at this size.
	coverJPEGQuality = 82
	// coverMaxPixels is the largest image that is decoded at all. Decoding
	// holds the whole picture in memory at four bytes a pixel, and a file can
	// claim any dimensions it likes; past this it is kept as it came.
	coverMaxPixels = 24_000_000
)

// shrinkGate lets one cover be decoded at a time. Eight covers are fetched at
// once (coverFetchLimit), and eight full-size bitmaps in memory together is
// several hundred megabytes on a machine that has better uses for them.
var shrinkGate sync.Mutex

// ShrinkCover returns a smaller rendering of a cover image — a JPEG no wider
// than coverMaxWidth — and the extension it is stored under. It reports false
// when the image is to be kept as it is: already light, of a format the
// standard library does not decode (WebP and AVIF, which are compact as they
// come, and GIF, which may be animated), too large to decode safely, turned
// by an EXIF orientation that re-encoding would lose, or not appreciably
// smaller once re-encoded.
func ShrinkCover(body []byte) (smaller []byte, ext string, ok bool) {
	config, format, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		return nil, "", false
	}
	// Said outright rather than left to which decoders happen to be
	// registered: any package in the binary can register another.
	if format != "jpeg" && format != "png" {
		return nil, "", false
	}
	if config.Width <= coverMaxWidth && len(body) <= coverLightBytes {
		return nil, "", false
	}
	if config.Width*config.Height > coverMaxPixels {
		return nil, "", false
	}
	if format == "jpeg" && jpegIsRotatedByEXIF(body) {
		return nil, "", false
	}

	shrinkGate.Lock()
	defer shrinkGate.Unlock()

	source, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		return nil, "", false
	}
	bounds := source.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= 0 || height <= 0 {
		return nil, "", false
	}

	// Onto white: JPEG has no transparency, and a cover with any is a logo or
	// a cut-out that was drawn to sit on a page.
	flat := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(flat, flat.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(flat, flat.Bounds(), source, bounds.Min, draw.Over)

	target := flat
	if width > coverMaxWidth {
		targetHeight := max(1, (height*coverMaxWidth+width/2)/width)
		target = resampleArea(flat, coverMaxWidth, targetHeight)
	}

	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, target, &jpeg.Options{Quality: coverJPEGQuality}); err != nil {
		return nil, "", false
	}
	// Worth it only for a real saving. A copy within a tenth of the original
	// is the original over again at a generation's loss — which is what a
	// cover this function already produced comes back as, so running it over
	// its own output changes nothing.
	if encoded.Len() > len(body)-len(body)/10 {
		return nil, "", false
	}
	return encoded.Bytes(), ".jpg", true
}

// resampleArea scales an opaque image down to width x height by area
// averaging: each destination pixel is the mean of the source area it covers,
// partial pixels at the edges of that area counted by the part covered. That
// is the right filter for making a picture smaller — every source pixel
// contributes, so nothing aliases — and at any ratio, where averaging whole
// pixels only is uneven for a reduction that is not a whole number.
//
// Two passes, across and then down, through a float buffer the width of the
// result and the height of the source.
func resampleArea(source *image.RGBA, width int, height int) *image.RGBA {
	sourceWidth, sourceHeight := source.Bounds().Dx(), source.Bounds().Dy()
	across := areaWeights(sourceWidth, width)
	down := areaWeights(sourceHeight, height)

	// Three channels: the image is opaque.
	rows := make([]float32, sourceHeight*width*3)
	for y := 0; y < sourceHeight; y++ {
		line := source.Pix[y*source.Stride : y*source.Stride+sourceWidth*4]
		out := rows[y*width*3 : (y+1)*width*3]
		for x, span := range across {
			var r, g, b float32
			for index, weight := range span.weights {
				pixel := line[(span.start+index)*4:]
				r += float32(pixel[0]) * weight
				g += float32(pixel[1]) * weight
				b += float32(pixel[2]) * weight
			}
			out[x*3], out[x*3+1], out[x*3+2] = r, g, b
		}
	}

	result := image.NewRGBA(image.Rect(0, 0, width, height))
	for y, span := range down {
		out := result.Pix[y*result.Stride : y*result.Stride+width*4]
		for x := 0; x < width; x++ {
			var r, g, b float32
			for index, weight := range span.weights {
				pixel := rows[((span.start+index)*width+x)*3:]
				r += pixel[0] * weight
				g += pixel[1] * weight
				b += pixel[2] * weight
			}
			out[x*4], out[x*4+1], out[x*4+2], out[x*4+3] = clampByte(r), clampByte(g), clampByte(b), 0xFF
		}
	}
	return result
}

// areaSpan is the run of source pixels one destination pixel covers, with the
// share of the result each contributes. The shares add up to one.
type areaSpan struct {
	start   int
	weights []float32
}

// areaWeights lays the destination pixels of one axis over the source's.
func areaWeights(sourceSize int, targetSize int) []areaSpan {
	scale := float64(sourceSize) / float64(targetSize)
	spans := make([]areaSpan, targetSize)
	for index := range spans {
		from, to := float64(index)*scale, float64(index+1)*scale
		first := int(from)
		last := min(int(to), sourceSize-1)
		if float64(last) == to && last > first {
			// The span ends exactly on a pixel boundary: that pixel is not in it.
			last--
		}
		weights := make([]float32, 0, last-first+1)
		for pixel := first; pixel <= last; pixel++ {
			covered := min(to, float64(pixel+1)) - max(from, float64(pixel))
			weights = append(weights, float32(covered/scale))
		}
		spans[index] = areaSpan{start: first, weights: weights}
	}
	return spans
}

func clampByte(value float32) uint8 {
	switch {
	case value <= 0:
		return 0
	case value >= 255:
		return 255
	default:
		return uint8(value + 0.5)
	}
}

// jpegIsRotatedByEXIF reports whether a JPEG carries an EXIF orientation other
// than upright. A browser turns such an image as the tag says; the decoder
// here does not, and the re-encoded copy has no tag, so it would be stored and
// shown on its side. It is left as it came instead.
//
// Only as much of the format is read as leads to that one tag: the APP1
// segment that starts "Exif", its TIFF header, and the first directory.
// Anything it cannot follow is taken as "no orientation".
func jpegIsRotatedByEXIF(body []byte) bool {
	// Segments follow the two-byte start-of-image marker, each a marker and a
	// big-endian length that counts itself.
	for offset := 2; offset+4 <= len(body); {
		if body[offset] != 0xFF {
			return false
		}
		marker := body[offset+1]
		// Start of scan: the headers are over.
		if marker == 0xDA {
			return false
		}
		length := int(binary.BigEndian.Uint16(body[offset+2:]))
		if length < 2 || offset+2+length > len(body) {
			return false
		}
		segment := body[offset+4 : offset+2+length]
		if marker == 0xE1 && bytes.HasPrefix(segment, []byte("Exif\x00\x00")) {
			return tiffOrientation(segment[6:]) > 1
		}
		offset += 2 + length
	}
	return false
}

// tiffOrientation reads the orientation tag (0x0112) from the first directory
// of a TIFF block; 0 when there is none or the block is not what it claims.
func tiffOrientation(tiff []byte) int {
	if len(tiff) < 8 {
		return 0
	}
	var order binary.ByteOrder
	switch string(tiff[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 0
	}
	directory := int(order.Uint32(tiff[4:]))
	if directory < 8 || directory+2 > len(tiff) {
		return 0
	}
	entries := int(order.Uint16(tiff[directory:]))
	for index := 0; index < entries; index++ {
		entry := directory + 2 + index*12
		if entry+12 > len(tiff) {
			return 0
		}
		if order.Uint16(tiff[entry:]) == 0x0112 {
			// A SHORT, stored in the first two bytes of the value field.
			return int(order.Uint16(tiff[entry+8:]))
		}
	}
	return 0
}
