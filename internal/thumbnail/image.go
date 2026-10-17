package thumbnail

import (
	"image"
	"image/color"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"os"
)

const maxThumbnailDimension = 640

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
	for y := range height {
		for x := range width {
			sx := bounds.Min.X + x*bounds.Dx()/width
			sy := bounds.Min.Y + y*bounds.Dy()/height
			value := source.At(sx, sy)
			// JPEG has no alpha. Composite transparent pixels over the same dark
			// neutral used by the gallery cards instead of producing black fringes.
			destination.Set(x, y, composite(value, color.RGBA{R: 30, G: 34, B: 40, A: 255}))
		}
	}
	out, err := os.Create(output)
	if err != nil {
		return err
	}
	if err := jpeg.Encode(out, destination, &jpeg.Options{Quality: 84}); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func composite(source color.Color, background color.RGBA) color.RGBA {
	r, g, b, a := source.RGBA()
	alpha := uint64(a)
	inverse := uint64(0xffff) - alpha
	return color.RGBA{
		R: uint8((uint64(r) + uint64(background.R)*0x101*inverse/0xffff) >> 8),
		G: uint8((uint64(g) + uint64(background.G)*0x101*inverse/0xffff) >> 8),
		B: uint8((uint64(b) + uint64(background.B)*0x101*inverse/0xffff) >> 8),
		A: 255,
	}
}
