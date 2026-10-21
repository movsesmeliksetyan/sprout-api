package receipts_test

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/receipts"
)

// picture is a w×h image, red in its top-left quarter and white elsewhere.
func picture(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.White)
			if x < w/4 && y < h/4 {
				img.Set(x, y, color.RGBA{R: 255, A: 255})
			}
		}
	}
	return img
}

func jpegOf(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}))
	return buf.Bytes()
}

// withOrientation puts an EXIF segment with the orientation tag right
// after a JPEG's start marker.
func withOrientation(data []byte, orientation uint16) []byte {
	tiff := []byte("MM\x00\x2a\x00\x00\x00\x08")
	tiff = binary.BigEndian.AppendUint16(tiff, 1) // one entry
	tiff = binary.BigEndian.AppendUint16(tiff, 0x0112)
	tiff = binary.BigEndian.AppendUint16(tiff, 3) // SHORT
	tiff = binary.BigEndian.AppendUint32(tiff, 1)
	tiff = binary.BigEndian.AppendUint16(tiff, orientation)
	tiff = append(tiff, 0, 0, 0, 0, 0, 0)
	segment := append([]byte("Exif\x00\x00"), tiff...)
	out := append([]byte{}, data[:2]...)
	out = append(out, 0xff, 0xe1)
	out = binary.BigEndian.AppendUint16(out, uint16(len(segment)+2))
	out = append(out, segment...)
	return append(out, data[2:]...)
}

func decoded(t *testing.T, data []byte) image.Image {
	t.Helper()
	img, err := jpeg.Decode(bytes.NewReader(data))
	require.NoError(t, err)
	return img
}

func TestNormalizeImage(t *testing.T) {
	t.Parallel()
	t.Run("a large photo is scaled to its long edge", func(t *testing.T) {
		t.Parallel()
		out, err := receipts.NormalizeImage(jpegOf(t, picture(3000, 1200)))
		require.NoError(t, err)
		assert.Equal(t, "image/jpeg", out.MediaType)
		assert.Equal(t, image.Pt(2000, 800), decoded(t, out.Data).Bounds().Size())
	})
	t.Run("a small one keeps its size", func(t *testing.T) {
		t.Parallel()
		out, err := receipts.NormalizeImage(jpegOf(t, picture(600, 900)))
		require.NoError(t, err)
		assert.Equal(t, image.Pt(600, 900), decoded(t, out.Data).Bounds().Size())
	})
	t.Run("a turned photo is put upright", func(t *testing.T) {
		t.Parallel()
		// Orientation 6: the camera was turned; the picture must be turned
		// clockwise, which moves the top-left corner to the top-right.
		out, err := receipts.NormalizeImage(withOrientation(jpegOf(t, picture(40, 20)), 6))
		require.NoError(t, err)
		img := decoded(t, out.Data)
		assert.Equal(t, image.Pt(20, 40), img.Bounds().Size())
		r, g, _, _ := img.At(18, 2).RGBA()
		assert.Greater(t, r, g*2, "red at the top right")
	})
	t.Run("the metadata is dropped", func(t *testing.T) {
		t.Parallel()
		out, err := receipts.NormalizeImage(withOrientation(jpegOf(t, picture(10, 10)), 1))
		require.NoError(t, err)
		assert.NotContains(t, string(out.Data), "Exif")
	})
	t.Run("PNG", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		require.NoError(t, png.Encode(&buf, picture(50, 30)))
		out, err := receipts.NormalizeImage(buf.Bytes())
		require.NoError(t, err)
		assert.Equal(t, image.Pt(50, 30), decoded(t, out.Data).Bounds().Size())
	})
	t.Run("HEIC", func(t *testing.T) {
		t.Parallel()
		data, err := os.ReadFile("../../testdata/receipts/formats/sample.heic")
		require.NoError(t, err)
		out, err := receipts.NormalizeImage(data)
		require.NoError(t, err)
		assert.Positive(t, decoded(t, out.Data).Bounds().Dx())
	})
	t.Run("not an image", func(t *testing.T) {
		t.Parallel()
		for _, data := range [][]byte{[]byte("%PDF-1.7"), {0xff, 0xd8, 0xff, 0x00}, nil} {
			_, err := receipts.NormalizeImage(data)
			assert.ErrorIs(t, err, receipts.ErrNotAnImage)
		}
	})
}
