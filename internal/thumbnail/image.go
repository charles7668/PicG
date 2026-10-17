package thumbnail

import (
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"os"

	xdraw "golang.org/x/image/draw"
)

const maxThumbnailDimension = 640

var thumbnailBackground = color.RGBA{R: 30, G: 34, B: 40, A: 255}

// RenderImage handles formats supported by Go's standard image decoders.
// Callers can fall back to FFmpeg for formats such as AVIF and WebP.
func RenderImage(input, output string) error {
	file, err := os.Open(input)
	if err != nil {
		return err
	}
	defer file.Close()
	source, _, err := image.Decode(file)
	if err != nil {
		return err
	}
	bounds := source.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width > maxThumbnailDimension || height > maxThumbnailDimension {
		scale := float64(maxThumbnailDimension) / float64(width)
		if height > width {
			scale = float64(maxThumbnailDimension) / float64(height)
		}
		width = max(1, int(float64(width)*scale))
		height = max(1, int(float64(height)*scale))
	}
	destination := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(destination, destination.Bounds(), &image.Uniform{C: thumbnailBackground}, image.Point{}, draw.Src)
	xdraw.ApproxBiLinear.Scale(destination, destination.Bounds(), source, bounds, xdraw.Over, nil)
	out, err := os.Create(output)
	if err != nil {
		return err
	}
	if err := jpeg.Encode(out, destination, &jpeg.Options{Quality: 78}); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
