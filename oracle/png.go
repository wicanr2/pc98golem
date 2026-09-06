package oracle

import (
	"image"
	"image/color"
	"image/png"
	"os"
)

// WritePNG 把目前的畫面存成 PNG（色號配上目前的調色盤）。
//
// **給人看的，不是驗收用的。** 逐點比對一律在色號空間做
// （`docs/spec/005` §3.3）——PNG 經過調色盤，而調色盤有循環動畫。
func (o *Oracle) WritePNG(path string) error {
	pal := o.Palette()
	p := make(color.Palette, 256)
	for i := range pal {
		p[i] = color.RGBA{pal[i][0], pal[i][1], pal[i][2], 255}
	}
	img := image.NewPaletted(image.Rect(0, 0, Width, Height), p)
	copy(img.Pix, o.video())
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}
