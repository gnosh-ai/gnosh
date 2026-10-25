package tools

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
	"image/png"

	"golang.org/x/image/draw"
)

// Images go to the model as Claude Code sends them: at most maxImageSide
// pixels a side and maxImageBytes, scaled down to fit, rather than
// refused over 200 KB, as most screenshots were.
const (
	maxImageSide  = 2000
	maxImageBytes = 3_932_160 // 3.75 MB, 5 MB once base64-encoded
	// maxImagePixels bounds the images decoded to be scaled: 50
	// megapixels take 200 MB as RGBA.
	maxImagePixels = 50_000_000
	// maxImageFileBytes bounds the image files read at all.
	maxImageFileBytes = 64 << 20
)

// errImageTooLarge is an image too large to send that could not be
// scaled down to fit.
var errImageTooLarge = errors.New("image too large")

// fitImage is data, an image of mimeType, as it can go to the model: as
// it is when it fits, else scaled down to at most maxImageSide pixels a
// side, and, when that is still over maxImageBytes, re-encoded as a JPEG
// of falling quality. An image it cannot decode (WebP, say) goes as it
// is when it is not over maxImageBytes, and is errImageTooLarge when it
// is.
func fitImage(data []byte, mimeType string) ([]byte, string, error) {
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		if len(data) > maxImageBytes {
			return nil, "", errImageTooLarge
		}
		return data, mimeType, nil
	}
	if len(data) <= maxImageBytes && config.Width <= maxImageSide && config.Height <= maxImageSide {
		return data, mimeType, nil
	}
	if config.Width*config.Height > maxImagePixels {
		return nil, "", errImageTooLarge
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", errImageTooLarge
	}
	img = scaleToFit(img, maxImageSide)
	if mimeType == "image/png" {
		var out bytes.Buffer
		if png.Encode(&out, img) == nil && out.Len() <= maxImageBytes {
			return out.Bytes(), "image/png", nil
		}
	}
	for _, quality := range []int{85, 70, 50, 30} {
		var out bytes.Buffer
		if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: quality}); err != nil {
			return nil, "", err
		}
		if out.Len() <= maxImageBytes {
			return out.Bytes(), "image/jpeg", nil
		}
	}
	return nil, "", errImageTooLarge
}

// scaleToFit scales img down, keeping its proportions, so that neither
// side is over side pixels. An image that fits is returned as it is.
func scaleToFit(img image.Image, side int) image.Image {
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w <= side && h <= side {
		return img
	}
	if w >= h {
		w, h = side, max(1, h*side/w)
	} else {
		w, h = max(1, w*side/h), side
	}
	scaled := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(scaled, scaled.Bounds(), img, bounds, draw.Src, nil)
	return scaled
}
