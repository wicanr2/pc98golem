package mscdrv

import (
	"encoding/binary"
	"fmt"
	"math"
	"sort"

	"github.com/wicanr2/pc98golem/internal/opn"
)

// 演奏資料區塊裡的命令。framing 只有三種寬度，判準是「每個區塊都要剛好在
// 它宣告的長度收尾」——這是從原版語料量出來的，不是猜的（spec 005）。
const (
	cmdRest          = 0x80
	cmdRegisterWrite = 0x81 // 81 <暫存器> <值>
	cmdGate          = 0x82
	cmdTempo         = 0x84
	cmdParameter     = 0x85 // 85 <種類> <指標16> <兩位元組尾巴>
	cmdNoOperand87   = 0x87
	cmdVolume        = 0x8A
	noteMax          = 0x60
)

// LowestNoteCode 是音高碼的下限；八度換算用 `(碼 − LowestNoteCode) / 12`。
const LowestNoteCode = 3

// TicksPerQuarter 是四分音符的時值，由時值分佈量出來（12／24／48／96／192
// 全是它的整數倍或整除數）。
const TicksPerQuarter = 24

// commandWidth 回傳一個區塊命令的總位元組數。
func commandWidth(opcode byte) int {
	switch {
	case opcode == cmdParameter:
		return 6
	case opcode == cmdRegisterWrite:
		return 3
	case opcode == cmdNoOperand87:
		return 1
	default:
		return 2
	}
}

// Event 是一個排好時間的演奏事件。
type Event struct {
	Tick    uint64
	Channel int
	Opcode  byte
	Note    byte
	Value   byte
	Reg     byte
	RegVal  byte
	KeyOff  bool
}

// RenderOptions 控制合成。
type RenderOptions struct {
	ClockHz      float64 // 預設 PC98YM2203ClockHz
	SampleRate   float64 // 預設 44100
	DefaultTempo byte    // 預設 120
	DefaultGate  byte    // 預設 7（分母 8）
	// LowestNoteMIDI 是音高碼 LowestNoteCode 對應的 MIDI 音高。
	// **這是假說**：拆法量得出來，絕對音高在音源 BIOS 的音高表裡。預設 24。
	LowestNoteMIDI int
	MaxSeconds     float64 // 預設 120
}

// PC98YM2203ClockHz 是 PC-98 上 YM2203 的主頻。
const PC98YM2203ClockHz = 3_993_600

func (o *RenderOptions) applyDefaults() {
	if o.ClockHz == 0 {
		o.ClockHz = PC98YM2203ClockHz
	}
	if o.SampleRate == 0 {
		o.SampleRate = 44100
	}
	if o.DefaultTempo == 0 {
		o.DefaultTempo = 120
	}
	if o.DefaultGate == 0 {
		o.DefaultGate = 7
	}
	if o.LowestNoteMIDI == 0 {
		o.LowestNoteMIDI = 24
	}
	if o.MaxSeconds == 0 {
		o.MaxSeconds = 120
	}
}

// Events 把抽出來的區塊解成排好時間的事件。
func (r Result) Events(fmChannels int) ([]Event, map[byte]int, error) {
	unknown := map[byte]int{}
	var events []Event
	for channel, blocks := range r.Channels {
		var tick uint64
		gate := byte(0)
		for _, block := range blocks {
			for at := 0; at < len(block.Bytes); {
				opcode := block.Bytes[at]
				width := commandWidth(opcode)
				if at+width > len(block.Bytes) {
					return nil, unknown, fmt.Errorf(
						"區塊 $%04X 的命令 $%02X 越過區塊尾端", block.Offset, opcode)
				}
				event := Event{Tick: tick, Channel: channel, Opcode: opcode}
				switch {
				case opcode <= noteMax:
					event.Note = opcode
					duration := uint64(block.Bytes[at+1])
					events = append(events, event)
					sounding := duration
					if gate != 0 {
						sounding = duration * uint64(gate) / 8
					}
					if sounding == 0 {
						sounding = duration
					}
					events = append(events, Event{
						Tick: tick + sounding, Channel: channel,
						Opcode: opcode, Note: opcode, KeyOff: true,
					})
					tick += duration
				case opcode == cmdRest:
					events = append(events, event)
					tick += uint64(block.Bytes[at+1])
				case opcode == cmdRegisterWrite:
					event.Reg, event.RegVal = block.Bytes[at+1], block.Bytes[at+2]
					events = append(events, event)
				case opcode == cmdGate:
					gate = block.Bytes[at+1]
					event.Value = gate
					events = append(events, event)
				case opcode == cmdTempo, opcode == cmdVolume:
					event.Value = block.Bytes[at+1]
					events = append(events, event)
				case opcode == cmdParameter:
					event.Value = block.Bytes[at+1]
					events = append(events, event)
				default:
					unknown[opcode]++
				}
				at += width
			}
		}
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].Tick < events[j].Tick })
	return events, unknown, nil
}

// Render 把事件合成成 16 位元單聲道 PCM。
func Render(events []Event, fmChannels int, options RenderOptions) ([]int16, float64, error) {
	options.applyDefaults()
	chip := opn.New(options.ClockHz, options.SampleRate)
	// 一組通用 FM 音色：原版的音色參數塊是 NEC 的格式，還沒解。
	primePatch(chip, fmChannels)

	tempo := options.DefaultTempo
	limit := int(options.MaxSeconds * options.SampleRate)
	var samples []int16
	var lastTick uint64

	for _, event := range events {
		if event.Tick > lastTick {
			seconds := 60.0 / (float64(tempo) * TicksPerQuarter) * float64(event.Tick-lastTick)
			count := int(math.Round(seconds * options.SampleRate))
			if len(samples)+count > limit {
				count = limit - len(samples)
			}
			if count > 0 {
				samples = append(samples, chip.Render(count)...)
			}
			lastTick = event.Tick
			if len(samples) >= limit {
				break
			}
		}
		applyEvent(chip, event, fmChannels, &tempo, options)
	}
	return samples, float64(len(samples)) / options.SampleRate, nil
}

func primePatch(chip *opn.Chip, fmChannels int) {
	for channel := 0; channel < fmChannels && channel < 3; channel++ {
		base := byte(channel)
		for slot := byte(0); slot < 4; slot++ {
			offset := slot*4 + base
			chip.Write(0x30+offset, 0x01) // DT=0 MUL=1
			chip.Write(0x40+offset, 0x20)
			chip.Write(0x50+offset, 0x1F) // 快 attack
			chip.Write(0x60+offset, 0x0A)
			chip.Write(0x70+offset, 0x04)
			chip.Write(0x80+offset, 0x2F)
		}
		chip.Write(0x40+0x04+base, 0x00) // 兩個載波全開
		chip.Write(0x40+0x0C+base, 0x00)
		chip.Write(0xB0+base, 3<<3|4) // 回授 3、演算法 4
	}
	chip.Write(0x07, 0x38) // SSG：三個音調都開，雜訊關
}

func applyEvent(
	chip *opn.Chip, event Event, fmChannels int, tempo *byte, options RenderOptions,
) {
	fm := event.Channel < fmChannels
	switch {
	case event.Opcode == cmdTempo:
		*tempo = event.Value
	case event.Opcode == cmdVolume:
		if fm && event.Channel < 3 {
			level := byte(0)
			if event.Value < 127 {
				level = 127 - event.Value
			}
			chip.Write(0x44+byte(event.Channel), level)
			chip.Write(0x4C+byte(event.Channel), level)
		} else if ssg := event.Channel - fmChannels; ssg >= 0 && ssg < 3 {
			chip.Write(0x08+byte(ssg), event.Value&0x0F)
		}
	case event.Opcode == cmdRegisterWrite:
		chip.Write(event.Reg, event.RegVal)
	case event.Opcode == cmdRest:
		keyOff(chip, event.Channel, fm, fmChannels)
	case event.Opcode <= noteMax:
		if event.KeyOff {
			keyOff(chip, event.Channel, fm, fmChannels)
			return
		}
		frequency := noteFrequency(event.Note, options.LowestNoteMIDI)
		if frequency <= 0 {
			return
		}
		if fm && event.Channel < 3 {
			block, number := fnumber(frequency, options.ClockHz)
			chip.Write(0xA4+byte(event.Channel), block<<3|byte(number>>8))
			chip.Write(0xA0+byte(event.Channel), byte(number))
			chip.Write(0x28, 0xF0|byte(event.Channel))
			return
		}
		if ssg := event.Channel - fmChannels; ssg >= 0 && ssg < 3 {
			period := int(math.Round(options.ClockHz / opn.SSGClockDivider / (16 * frequency)))
			if period < 1 {
				period = 1
			} else if period > 0xFFF {
				period = 0xFFF
			}
			chip.Write(byte(ssg*2), byte(period))
			chip.Write(byte(ssg*2+1), byte(period>>8)&0x0F)
		}
	}
}

func keyOff(chip *opn.Chip, channel int, fm bool, fmChannels int) {
	if fm && channel < 3 {
		chip.Write(0x28, byte(channel))
		return
	}
	if ssg := channel - fmChannels; ssg >= 0 && ssg < 3 {
		chip.Write(0x08+byte(ssg), 0)
	}
}

func noteFrequency(note byte, lowestMIDI int) float64 {
	if int(note) < LowestNoteCode {
		return 0
	}
	midi := lowestMIDI + int(note) - LowestNoteCode
	return 440.0 * math.Pow(2, float64(midi-69)/12.0)
}

func fnumber(frequency, clockHz float64) (byte, uint16) {
	base := clockHz / opn.FMClockDivider
	for block := 0; block < 8; block++ {
		value := frequency * math.Exp2(20) / base / math.Exp2(float64(block)-1)
		if value < 2048 {
			return byte(block), uint16(math.Round(value))
		}
	}
	return 7, 2047
}

// WriteWAV 寫 16 位元單聲道 WAV。
func WriteWAV(samples []int16, sampleRate int) []byte {
	body := make([]byte, len(samples)*2)
	for i, s := range samples {
		binary.LittleEndian.PutUint16(body[i*2:], uint16(s))
	}
	header := make([]byte, 44)
	copy(header[0:], "RIFF")
	binary.LittleEndian.PutUint32(header[4:], uint32(36+len(body)))
	copy(header[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(header[16:], 16)
	binary.LittleEndian.PutUint16(header[20:], 1)
	binary.LittleEndian.PutUint16(header[22:], 1)
	binary.LittleEndian.PutUint32(header[24:], uint32(sampleRate))
	binary.LittleEndian.PutUint32(header[28:], uint32(sampleRate*2))
	binary.LittleEndian.PutUint16(header[32:], 2)
	binary.LittleEndian.PutUint16(header[34:], 16)
	copy(header[36:], "data")
	binary.LittleEndian.PutUint32(header[40:], uint32(len(body)))
	return append(header, body...)
}
