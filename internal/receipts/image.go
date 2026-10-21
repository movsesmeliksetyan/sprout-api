package receipts

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"

	"github.com/gen2brain/heic"
	"golang.org/x/image/draw"

	"github.com/movsesmeliksetyan/sprout-api/internal/llm"
)

// MaxLongEdge is the longest side, in pixels, of the image the model sees.
const MaxLongEdge = 2000

// jpegQuality is the quality the image is sent at.
const jpegQuality = 85

// ErrNotAnImage is a file that is not a JPEG, PNG or HEIC image.
var ErrNotAnImage = errors.New("receipts: not an image Sprout can read")

// NormalizeImage prepares a receipt photo for the model: decoded (JPEG,
// PNG or HEIC), turned upright by its EXIF orientation, scaled down to
// MaxLongEdge on its long side and written as a JPEG — which also drops the
// metadata, the photo's location among it.
func NormalizeImage(data []byte) (llm.Image, error) {
	img, err := decode(data)
	if err != nil {
		return llm.Image{}, err
	}
	img = scale(img, MaxLongEdge)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return llm.Image{}, fmt.Errorf("receipts: encode image: %w", err)
	}
	return llm.Image{MediaType: "image/jpeg", Data: buf.Bytes()}, nil
}

func decode(data []byte) (image.Image, error) {
	switch {
	case bytes.HasPrefix(data, []byte{0xff, 0xd8, 0xff}):
		img, err := jpeg.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrNotAnImage, err)
		}
		return orient(img, jpegOrientation(data)), nil
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		img, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrNotAnImage, err)
		}
		return img, nil
	case isHEIF(data):
		// The decoder applies the image's own rotation and mirroring.
		img, err := heic.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrNotAnImage, err)
		}
		return img, nil
	}
	return nil, ErrNotAnImage
}

// isHEIF reports whether data starts with an ISO BMFF ftyp box of a HEIF
// brand.
func isHEIF(data []byte) bool {
	if len(data) < 12 || string(data[4:8]) != "ftyp" {
		return false
	}
	switch string(data[8:12]) {
	case "heic", "heix", "heim", "heis", "hevc", "hevx", "mif1", "msf1":
		return true
	}
	return false
}

// scale shrinks img so its long side is at most maxEdge.
func scale(img image.Image, maxEdge int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if max(w, h) <= maxEdge {
		return img
	}
	if w >= h {
		w, h = maxEdge, h*maxEdge/w
	} else {
		w, h = w*maxEdge/h, maxEdge
	}
	out := image.NewRGBA(image.Rect(0, 0, max(w, 1), max(h, 1)))
	draw.CatmullRom.Scale(out, out.Bounds(), img, b, draw.Src, nil)
	return out
}

// orient turns img upright from its EXIF orientation (1–8; 1 is upright).
func orient(img image.Image, orientation int) image.Image {
	if orientation < 2 || orientation > 8 {
		return img
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	swap := orientation >= 5
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	if swap {
		out = image.NewRGBA(image.Rect(0, 0, h, w))
	}
	for y := range h {
		for x := range w {
			var dx, dy int
			switch orientation {
			case 2: // mirrored
				dx, dy = w-1-x, y
			case 3: // upside down
				dx, dy = w-1-x, h-1-y
			case 4: // upside down, mirrored
				dx, dy = x, h-1-y
			case 5: // mirrored, turned left
				dx, dy = y, x
			case 6: // turned left: rotate clockwise
				dx, dy = h-1-y, x
			case 7: // mirrored, turned right
				dx, dy = h-1-y, w-1-x
			case 8: // turned right: rotate anticlockwise
				dx, dy = y, w-1-x
			}
			out.Set(dx, dy, img.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return out
}

// jpegOrientation reads the EXIF orientation of a JPEG; 1 when it has
// none or it cannot be read.
func jpegOrientation(data []byte) int {
	for i := 2; i+4 <= len(data); {
		if data[i] != 0xff {
			return 1
		}
		marker := data[i+1]
		size := int(binary.BigEndian.Uint16(data[i+2:]))
		if marker == 0xda || size < 2 || i+2+size > len(data) {
			return 1 // the image data starts: no EXIF before it
		}
		segment := data[i+4 : i+2+size]
		if marker == 0xe1 && bytes.HasPrefix(segment, []byte("Exif\x00\x00")) {
			return tiffOrientation(segment[6:])
		}
		i += 2 + size
	}
	return 1
}

// tiffOrientation reads tag 0x0112 from the first IFD of a TIFF header.
func tiffOrientation(tiff []byte) int {
	if len(tiff) < 8 {
		return 1
	}
	var order binary.ByteOrder
	switch string(tiff[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 1
	}
	ifd := int(order.Uint32(tiff[4:]))
	if ifd+2 > len(tiff) {
		return 1
	}
	entries := int(order.Uint16(tiff[ifd:]))
	for e := range entries {
		at := ifd + 2 + e*12
		if at+12 > len(tiff) {
			return 1
		}
		if order.Uint16(tiff[at:]) == 0x0112 {
			return int(order.Uint16(tiff[at+8:]))
		}
	}
	return 1
}
