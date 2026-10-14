package handlers

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"testing"
)

func tinyGIF(frames int) []byte {
	pal := color.Palette{color.Black, color.White}
	g := &gif.GIF{}
	for i := 0; i < frames; i++ {
		img := image.NewPaletted(image.Rect(0, 0, 32, 32), pal)
		img.SetColorIndex(i, i, 1)
		g.Image = append(g.Image, img)
		g.Delay = append(g.Delay, 10)
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, g); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func TestGIFIsRebuiltWithoutHiddenBlocks(t *testing.T) {
	src := tinyGIF(3)
	if src[len(src)-1] != 0x3B {
		t.Fatalf("test gif does not end with a trailer")
	}
	dirty := append([]byte{}, src[:13]...)
	gctSize := 0
	if src[10]&0x80 != 0 {
		gctSize = 3 * (1 << (uint(src[10]&7) + 1))
	}
	dirty = append(dirty, src[13:13+gctSize]...)
	dirty = append(dirty, 0x21, 0xFE, 0x0B)
	dirty = append(dirty, []byte("SECRETPAYLOD")[:11]...)
	dirty = append(dirty, 0x00)
	dirty = append(dirty, src[13+gctSize:]...)
	dirty = append(dirty, []byte("<script>trailing junk</script>")...)
	clean, frames := cleanGIF(dirty)
	if frames != 3 {
		t.Fatalf("frames = %d", frames)
	}
	if bytes.Contains(clean, []byte("SECRETPAYLO")) || bytes.Contains(clean, []byte("<script>")) {
		t.Fatalf("hidden data survived the rebuild")
	}
	g, err := gif.DecodeAll(bytes.NewReader(clean))
	if err != nil || len(g.Image) != 3 {
		t.Fatalf("rebuilt gif does not decode: %v", err)
	}
	if _, n := cleanGIF(dirty[:len(dirty)-40]); n != 0 {
		t.Fatalf("a GIF without its trailer must be refused")
	}
}
