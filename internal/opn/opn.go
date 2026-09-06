// Package opn 是 YM2203（OPN）的合成子集：三個四運算元 FM 聲道與三個 SSG
// 方波聲道。
//
// **這是近似，不是週期精確的。** 包絡用線性 dB 段近似，SSG 只做音調與音量
// （雜訊與硬體包絡沒做）。判準是「旋律、節奏、速度與原版資料一致」，
// 不是「波形與真機相同」（spec 006 §3）。
//
// 本套件不認識音樂格式，只吃暫存器寫入與時間。
package opn

import "math"

// FMClockDivider 是 FM 部的分頻：原生取樣率 ＝ 主頻 / 72。
const FMClockDivider = 72

// SSGClockDivider 是 SSG 部的分頻。
const SSGClockDivider = 4

const (
	sineBits  = 10
	sineSize  = 1 << sineBits
	maxAtten  = 1023 // 以 1/8 dB 為單位的衰減上限
	envelopeK = 0.09 // 包絡速率換算常數，由聽感校過
)

// Chip 是一顆合成中的 OPN。
type Chip struct {
	clockHz    float64
	sampleRate float64

	regs [256]byte

	fm  [3]fmChannel
	ssg [3]ssgChannel

	sine [sineSize]float64
}

type fmOperator struct {
	phase      float64
	increment  float64
	multiple   float64
	detune     int
	totalLevel float64 // 線性增益 0..1
	attack     float64
	decay      float64
	sustain    float64 // 線性增益
	release    float64
	envelope   float64 // 目前的線性增益
	state      int     // 0 靜止、1 attack、2 decay、3 sustain、4 release
}

type fmChannel struct {
	op        [4]fmOperator
	algorithm byte
	feedback  float64
	fnumber   uint16
	block     byte
	last      [2]float64
	keyOn     bool
}

type ssgChannel struct {
	period  int
	counter int
	level   float64
	output  float64
	enabled bool
}

// New 造一顆晶片。sampleRate 是輸出取樣率；內部就用它，不另外重取樣。
func New(clockHz, sampleRate float64) *Chip {
	c := &Chip{clockHz: clockHz, sampleRate: sampleRate}
	for i := range c.sine {
		c.sine[i] = math.Sin(2 * math.Pi * float64(i) / sineSize)
	}
	for channel := range c.fm {
		for op := range c.fm[channel].op {
			c.fm[channel].op[op].multiple = 1
			c.fm[channel].op[op].totalLevel = 1
			c.fm[channel].op[op].sustain = 1
			c.fm[channel].op[op].attack = 0.02
			c.fm[channel].op[op].decay = 0.0005
			c.fm[channel].op[op].release = 0.002
		}
	}
	return c
}

// NativeSampleRate 是 FM 部的原生取樣率，拿來當渲染的時間基準。
func NativeSampleRate(clockHz float64) float64 { return clockHz / FMClockDivider }

// Write 寫一個暫存器。
func (c *Chip) Write(register, value byte) {
	c.regs[register] = value
	switch {
	case register == 0x28: // key on/off
		channel := int(value & 3)
		if channel > 2 {
			return
		}
		if value&0xF0 != 0 {
			c.keyOn(channel)
		} else {
			c.keyOff(channel)
		}
	case register >= 0x30 && register < 0x40:
		channel, slot := int(register&3), int((register>>2)&3)
		if channel > 2 {
			return
		}
		multiple := float64(value & 0x0F)
		if multiple == 0 {
			multiple = 0.5
		}
		c.fm[channel].op[slot].multiple = multiple
		c.fm[channel].op[slot].detune = int((value >> 4) & 7)
	case register >= 0x40 && register < 0x50:
		channel, slot := int(register&3), int((register>>2)&3)
		if channel > 2 {
			return
		}
		// TL 是 0..127 的衰減，每格 0.75 dB。
		c.fm[channel].op[slot].totalLevel = math.Pow(10, -float64(value&0x7F)*0.75/20)
	case register >= 0x50 && register < 0x60:
		channel, slot := int(register&3), int((register>>2)&3)
		if channel > 2 {
			return
		}
		c.fm[channel].op[slot].attack = rateToStep(value&0x1F, c.sampleRate) * 12
	case register >= 0x60 && register < 0x70:
		channel, slot := int(register&3), int((register>>2)&3)
		if channel > 2 {
			return
		}
		c.fm[channel].op[slot].decay = rateToStep(value&0x1F, c.sampleRate)
	case register >= 0x70 && register < 0x80:
		channel, slot := int(register&3), int((register>>2)&3)
		if channel > 2 {
			return
		}
		c.fm[channel].op[slot].release = rateToStep(value&0x1F, c.sampleRate)
	case register >= 0x80 && register < 0x90:
		channel, slot := int(register&3), int((register>>2)&3)
		if channel > 2 {
			return
		}
		level := float64((value>>4)&0x0F) * 3 // SL：每格 3 dB
		c.fm[channel].op[slot].sustain = math.Pow(10, -level/20)
		c.fm[channel].op[slot].release = rateToStep((value&0x0F)<<1|1, c.sampleRate)
	case register >= 0xA0 && register < 0xA4:
		channel := int(register & 3)
		if channel > 2 {
			return
		}
		c.fm[channel].fnumber = c.fm[channel].fnumber&0x0700 | uint16(value)
		c.refresh(channel)
	case register >= 0xA4 && register < 0xA8:
		channel := int(register & 3)
		if channel > 2 {
			return
		}
		c.fm[channel].fnumber = c.fm[channel].fnumber&0x00FF | uint16(value&7)<<8
		c.fm[channel].block = (value >> 3) & 7
		c.refresh(channel)
	case register >= 0xB0 && register < 0xB4:
		channel := int(register & 3)
		if channel > 2 {
			return
		}
		c.fm[channel].algorithm = value & 7
		c.fm[channel].feedback = float64((value>>3)&7) / 7 * 2
	case register < 0x06: // SSG 音調週期
		channel := int(register >> 1)
		fine := int(c.regs[channel*2])
		coarse := int(c.regs[channel*2+1] & 0x0F)
		c.ssg[channel].period = coarse<<8 | fine
	case register == 0x07: // SSG 混音器：位元為 0 代表開
		for channel := range c.ssg {
			c.ssg[channel].enabled = value&(1<<channel) == 0
		}
	case register >= 0x08 && register < 0x0B:
		channel := int(register - 0x08)
		volume := value & 0x0F
		c.ssg[channel].level = 0
		if volume > 0 {
			c.ssg[channel].level = math.Pow(10, -float64(15-volume)*1.5/20)
		}
	}
}

// rateToStep 把 5 位元的速率換成每個取樣的線性增益變化量。
func rateToStep(rate byte, sampleRate float64) float64 {
	if rate == 0 {
		return 0
	}
	// 速率每加 4 大約快一倍，這是 OPN 包絡的基本形狀。
	seconds := 8.0 / math.Pow(2, float64(rate)/4) * envelopeK
	if seconds <= 0 {
		seconds = 1e-6
	}
	return 1.0 / (seconds * sampleRate)
}

func (c *Chip) refresh(channel int) {
	ch := &c.fm[channel]
	// OPN 的相位增量：F-Number × 2^(block−1) × 主頻 / (72 × 2^20)
	base := float64(ch.fnumber) * math.Exp2(float64(ch.block)-1) *
		c.clockHz / (FMClockDivider * math.Exp2(20))
	for slot := range ch.op {
		ch.op[slot].increment = base * ch.op[slot].multiple / c.sampleRate * sineSize
	}
}

func (c *Chip) keyOn(channel int) {
	ch := &c.fm[channel]
	ch.keyOn = true
	for slot := range ch.op {
		ch.op[slot].state = 1
		ch.op[slot].envelope = 0
		ch.op[slot].phase = 0
	}
	ch.last = [2]float64{}
}

func (c *Chip) keyOff(channel int) {
	ch := &c.fm[channel]
	ch.keyOn = false
	for slot := range ch.op {
		if ch.op[slot].state != 0 {
			ch.op[slot].state = 4
		}
	}
}

// Render 產生 n 個 16 位元單聲道樣本。
func (c *Chip) Render(n int) []int16 {
	out := make([]int16, n)
	ssgStep := c.clockHz / SSGClockDivider / 16 / c.sampleRate
	for i := range out {
		var sum float64
		for channel := range c.fm {
			sum += c.stepFM(channel)
		}
		for channel := range c.ssg {
			sum += c.stepSSG(channel, ssgStep)
		}
		value := sum * 8000
		if value > 32767 {
			value = 32767
		} else if value < -32768 {
			value = -32768
		}
		out[i] = int16(value)
	}
	return out
}

func (c *Chip) stepFM(channel int) float64 {
	ch := &c.fm[channel]
	var out [4]float64
	for slot := range ch.op {
		op := &ch.op[slot]
		c.advanceEnvelope(op)
		var modulation float64
		switch {
		case slot == 0:
			modulation = (ch.last[0] + ch.last[1]) / 2 * ch.feedback
		default:
			modulation = modulationFor(ch.algorithm, slot, &out)
		}
		op.phase += op.increment
		if op.phase >= sineSize {
			op.phase -= float64(int(op.phase/sineSize)) * sineSize
		}
		index := int(op.phase+modulation*sineSize/4) & (sineSize - 1)
		out[slot] = c.sine[index] * op.envelope * op.totalLevel
		if slot == 0 {
			ch.last[1], ch.last[0] = ch.last[0], out[0]
		}
	}
	var mix float64
	for _, slot := range carriers(ch.algorithm) {
		mix += out[slot]
	}
	return mix / 4
}

// modulationFor 是演算法的接線：回傳這個運算元的調變輸入。
// 槽位順序是 YM 的 S1、S3、S2、S4（暫存器順序），這裡用邏輯順序 0..3。
func modulationFor(algorithm byte, slot int, out *[4]float64) float64 {
	switch algorithm & 7 {
	case 0: // 1→2→3→4
		return out[slot-1]
	case 1: // (1+2)→3→4
		if slot == 2 {
			return out[0] + out[1]
		}
		if slot == 3 {
			return out[2]
		}
	case 2: // 1→3, (2+3)→4
		if slot == 2 {
			return out[0]
		}
		if slot == 3 {
			return out[1] + out[2]
		}
	case 3: // 1→2, (2+3)→4
		if slot == 1 {
			return out[0]
		}
		if slot == 3 {
			return out[1] + out[2]
		}
	case 4: // 1→2, 3→4
		if slot == 1 {
			return out[0]
		}
		if slot == 3 {
			return out[2]
		}
	case 5: // 1→2, 1→3, 1→4
		if slot > 0 {
			return out[0]
		}
	case 6: // 1→2，其餘獨立
		if slot == 1 {
			return out[0]
		}
	case 7: // 全部獨立
	}
	return 0
}

// carriers 回傳哪些槽位是載波。
func carriers(algorithm byte) []int {
	switch algorithm & 7 {
	case 0, 1, 2, 3:
		return []int{3}
	case 4:
		return []int{1, 3}
	case 5, 6:
		return []int{1, 2, 3}
	default:
		return []int{0, 1, 2, 3}
	}
}

func (c *Chip) advanceEnvelope(op *fmOperator) {
	switch op.state {
	case 1:
		op.envelope += op.attack
		if op.envelope >= 1 {
			op.envelope, op.state = 1, 2
		}
	case 2:
		op.envelope -= op.decay
		if op.envelope <= op.sustain {
			op.envelope, op.state = op.sustain, 3
		}
	case 4:
		op.envelope -= op.release
		if op.envelope <= 0 {
			op.envelope, op.state = 0, 0
		}
	}
}

func (c *Chip) stepSSG(channel int, step float64) float64 {
	s := &c.ssg[channel]
	if !s.enabled || s.period == 0 || s.level == 0 {
		return 0
	}
	s.counter++
	if float64(s.counter) >= float64(s.period)/step {
		s.counter = 0
		if s.output == 0 {
			s.output = 1
		} else {
			s.output = 0
		}
	}
	return (s.output*2 - 1) * s.level * 0.3
}
