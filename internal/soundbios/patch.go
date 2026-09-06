package soundbios

import "fmt"

// PatchBytes 是一塊音色參數的位元組數（BYTE 格式）。
const PatchBytes = 52

// 音色參數塊的版面。
//
// `SETPARABLOCK`（`AH=16h`）用 `DL` 選格式：`DL=0` 是 WORD 格式（100 位元組），
// `DL=1` 是同一組欄位的 BYTE 格式。這一族驅動用 BYTE 格式，所以是 52 位元組
// ——50 個欄位各一格，**LFO 速度仍是 WORD**（它之後的欄位整體位移一格），
// 尾端補一格。
//
// 版面是對值域對出來的：運算元遮罩恆為 `$0E`／`$0F`、KS 四格恰好落在 0..3、
// 三個保留欄位（+41、+46、+51）在全部音色裡都是 0。
const (
	patchFeedbackAlgorithm = 0
	patchLFOPitchDepth     = 26
	patchLFOSpeed          = 20
	patchAttackRate        = 1
	patchOperatorMask      = 5
	patchDecayRate         = 6
	patchLFOWaveform       = 10
	patchSustainRate       = 11
	patchReleaseRate       = 16
	patchSustainLevel      = 22
	patchOutputLevel       = 27
	patchKeyScale          = 32
	patchMultiple          = 37
	patchDetune            = 42
)

// physicalSlot 把邏輯運算元 1..4 對到 YM2203 暫存器的槽位（S1、S3、S2、S4）。
var physicalSlot = [4]byte{0, 2, 1, 3}

// Patch 是一塊解好的 FM 音色，存的是**已經換算成晶片值**的欄位。
//
// NEC 的速率與準位是反向刻度，音源 BIOS 在 `SETPARABLOCK` 裡換算：
//
//	KS/AR  = KS << 6 | (31 − AR 參數)
//	DR     = 31 − DR 參數
//	SR     = 31 − SR 參數
//	SL/RR  = (15 − SL 參數) << 4 | (15 − RR 參數)
//	TL     = 127 − OUTPUT_LEVEL
//
// **直接照抄的症狀不是報錯，是幾首曲子幾乎沒有聲音**——快的攻擊被讀成慢的、
// 響的準位被讀成靜的。
type Patch struct {
	Feedback, Algorithm, OperatorMask               byte
	// LFO 欄位。音高調變由渲染端套用。
	LFOWaveform   byte
	LFOSpeed      uint16
	LFOPitchDepth int8
	AttackRate, DecayRate, SustainRate, ReleaseRate [4]byte
	SustainLevel, OutputLevel, KeyScale, Multiple   [4]byte
	Detune                                          [4]byte
}

// DecodePatch 解一塊音色參數。
func DecodePatch(raw []byte) (Patch, error) {
	if len(raw) < PatchBytes {
		return Patch{}, fmt.Errorf("音色參數塊只有 %d 位元組，需要 %d", len(raw), PatchBytes)
	}
	patch := Patch{
		Feedback:     raw[patchFeedbackAlgorithm] >> 3 & 7,
		Algorithm:    raw[patchFeedbackAlgorithm] & 7,
		OperatorMask:  raw[patchOperatorMask],
		LFOWaveform:   raw[patchLFOWaveform],
		LFOSpeed:      uint16(raw[patchLFOSpeed]) | uint16(raw[patchLFOSpeed+1])<<8,
		LFOPitchDepth: int8(raw[patchLFOPitchDepth]),
	}
	for operator := 0; operator < 4; operator++ {
		patch.AttackRate[operator] = 31 - raw[patchAttackRate+operator]&0x1F
		patch.DecayRate[operator] = 31 - raw[patchDecayRate+operator]&0x1F
		patch.SustainRate[operator] = 31 - raw[patchSustainRate+operator]&0x1F
		patch.ReleaseRate[operator] = 15 - raw[patchReleaseRate+operator]&0x0F
		patch.SustainLevel[operator] = 15 - raw[patchSustainLevel+operator]&0x0F
		patch.OutputLevel[operator] = 127 - raw[patchOutputLevel+operator]&0x7F
		patch.KeyScale[operator] = raw[patchKeyScale+operator] & 0x03
		patch.Multiple[operator] = raw[patchMultiple+operator] & 0x0F
		// DETUNE 不先截成三位元：音源 BIOS 用 8 位元左移，−1 會變成 `F0h | MUL`。
		patch.Detune[operator] = raw[patchDetune+operator]
	}
	return patch, nil
}

// Program 把音色寫進某一個 FM 聲道的暫存器。
func (p Patch) Program(write func(register, value byte), channel int) {
	if channel < 0 || channel > 2 {
		return
	}
	for operator := 0; operator < 4; operator++ {
		base := physicalSlot[operator]*4 + byte(channel)
		write(0x30+base, p.Detune[operator]<<4|p.Multiple[operator])
		write(0x40+base, p.OutputLevel[operator])
		write(0x50+base, p.KeyScale[operator]<<6|p.AttackRate[operator])
		write(0x60+base, p.DecayRate[operator])
		write(0x70+base, p.SustainRate[operator])
		write(0x80+base, p.SustainLevel[operator]<<4|p.ReleaseRate[operator])
	}
	write(0xB0+byte(channel), p.Feedback<<3|p.Algorithm)
	// **不寫 `$B4`。** 那是 OPNA／OPN2 的左右聲道與 AMS／PMS 暫存器，
	// YM2203 沒有——真韌體從安裝到音序 240 格，一次都沒有碰過 `$B4`..`$B6`。
}

// KeyOn 是這個音色的 key-on 位元組。**不是永遠 `F0h`**——哪幾個運算元發聲
// 由音色的運算元遮罩決定。
func (p Patch) KeyOn(channel int) byte { return (p.OperatorMask&0x0F)<<4 | byte(channel) }

// Carriers 回傳載波的邏輯運算元序號。
func (p Patch) Carriers() []int {
	switch p.Algorithm & 7 {
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

// CarrierRegister 是某個載波的 TL 暫存器位址。
func CarrierRegister(operator, channel int) byte {
	return 0x40 + physicalSlot[operator]*4 + byte(channel)
}


// LFOPitch 把一個 LFO 取樣套到 F-Number 上。
//
// 兩段除法是音源 BIOS 計時器 ISR 的原樣。**深度小的時候結果會整個歸零**
// ——那不是實作漏了什麼，是原版本來就這樣：F-Number 600 配深度 127 也只
// 推得動 ±2。所以「調變有沒有接上」用聽的分不出來，要看暫存器寫入。
func LFOPitch(base uint16, sample int16, depth int8) uint16 {
	const fullScale = 0x7FFF
	scaled := int32(sample) * int32(depth) / fullScale
	scaled = scaled * int32(base) / fullScale
	value := int32(base) + scaled
	if value < 0 {
		value = 0
	} else if value > 2047 {
		value = 2047
	}
	return uint16(value)
}
