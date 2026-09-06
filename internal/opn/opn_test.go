package opn

import (
	"math"
	"testing"
)

// key-on 之後要出聲，key-off 之後要停。**這是最基本的一條**：
// 少了它，「合成器沒接上」與「曲子這一段本來就是靜的」分不出來。
func TestKeyOnProducesSoundAndKeyOffStops(t *testing.T) {
	chip := New(3_993_600, 44100)
	// 演算法 7（四個載波全開），全開的 TL 與快 attack。
	for slot := byte(0); slot < 4; slot++ {
		chip.Write(0x30+slot*4, 0x01)
		chip.Write(0x40+slot*4, 0x00)
		chip.Write(0x50+slot*4, 0x1F)
		chip.Write(0x60+slot*4, 0x00)
		chip.Write(0x80+slot*4, 0x00)
	}
	chip.Write(0xB0, 7)
	chip.Write(0xA4, 4<<3|1)
	chip.Write(0xA0, 0x00)
	chip.Write(0x28, 0xF0)

	loud := peak(chip.Render(4410))
	if loud < 1000 {
		t.Fatalf("key-on 之後的峰值只有 %d，等於沒出聲", loud)
	}
	chip.Write(0x28, 0x00)
	chip.Render(44100) // 讓 release 走完
	quiet := peak(chip.Render(4410))
	if quiet >= loud/4 {
		t.Errorf("key-off 之後峰值還有 %d（之前 %d），沒有停下來", quiet, loud)
	}
}

// F-Number 大一格，聽起來要高一點：用過零次數當粗略的音高量測。
func TestHigherFNumberGivesHigherPitch(t *testing.T) {
	measure := func(fnumber uint16) int {
		chip := New(3_993_600, 44100)
		for slot := byte(0); slot < 4; slot++ {
			chip.Write(0x40+slot*4, 0x00)
			chip.Write(0x50+slot*4, 0x1F)
			chip.Write(0x30+slot*4, 0x01)
		}
		chip.Write(0xB0, 7)
		chip.Write(0xA4, 4<<3|byte(fnumber>>8))
		chip.Write(0xA0, byte(fnumber))
		chip.Write(0x28, 0xF0)
		return crossings(chip.Render(22050))
	}
	low, high := measure(300), measure(1200)
	if high <= low {
		t.Errorf("F-Number 1200 的過零次數 %d 沒有比 300 的 %d 多", high, low)
	}
}

// SSG 的混音器關掉就不該有聲音。
func TestSSGRespectsTheMixer(t *testing.T) {
	chip := New(3_993_600, 44100)
	chip.Write(0x00, 0x00)
	chip.Write(0x01, 0x01) // 週期 0x100
	chip.Write(0x08, 0x0F) // 音量全開
	chip.Write(0x07, 0x39) // 位元 0 為 1 ＝ 聲道 A 關
	if got := peak(chip.Render(4410)); got > 100 {
		t.Errorf("混音器關掉了還有 %d 的峰值", got)
	}
	chip.Write(0x07, 0x38) // 三個音調都開
	if got := peak(chip.Render(4410)); got < 500 {
		t.Errorf("混音器打開之後峰值只有 %d", got)
	}
}

func peak(samples []int16) int {
	out := 0
	for _, s := range samples {
		if v := int(math.Abs(float64(s))); v > out {
			out = v
		}
	}
	return out
}

func crossings(samples []int16) int {
	out := 0
	for i := 1; i < len(samples); i++ {
		if (samples[i-1] < 0) != (samples[i] < 0) {
			out++
		}
	}
	return out
}
