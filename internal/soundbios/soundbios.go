// Package soundbios 是 NEC PC-9800 音源 BIOS（`INT D2h`）的軟體替身。
//
// 真的音源 BIOS 在 PC-9801-26K 音源板的 ROM 裡。我們沒有那顆 ROM，也不需要：
// MSCDRV 這一族驅動只用五個命令，而音高與時值都在曲子資料裡（spec 006）。
//
// **沒實作的命令會被記下來並回報**，不會靜靜變成 nop——不然「BIOS 少做一件事」
// 會看起來像「驅動沒要求那件事」。
package soundbios

import (
	"fmt"

	"github.com/wicanr2/pc98golem/internal/cpu"
	"github.com/wicanr2/pc98golem/internal/machine"
)

// Vector 是驅動慣用的中斷向量號碼；N88-BASIC 用的也是這個。
const Vector = 0xD2

// InterfaceSegment 是音源 BIOS 介面表所在的段。驅動靠 `CEE0:0004 == $00D2`
// 認出音源板。
const InterfaceSegment = 0xCEE0

// ROMBase／ROMBytes 是音源 BIOS ROM 的對映位置與大小。
const (
	ROMBase  = 0xCC000
	ROMBytes = 16384
)

// InstallROM 把一顆真的音源 BIOS ROM 映進去，**不接管 `INT D2h`**——
// 讓原版韌體自己跑。回傳的 BIOS 只用來看驅動有沒有裝好。
//
// 判準照驅動自己的檢查：`CEE0:0004` 要是 `$00D2`。版本不對的 dump 在這裡
// 就會被擋下來，而不是等到播放時才聽出怪聲。
func InstallROM(m *machine.Machine, rom []byte) (*BIOS, error) {
	if len(rom) != ROMBytes {
		return nil, fmt.Errorf("音源 BIOS ROM 是 %d 位元組，預期 %d", len(rom), ROMBytes)
	}
	m.WriteBytes(ROMBase, rom)
	base := uint32(InterfaceSegment) * 16
	if got := m.Read16(base + 4); got != Vector {
		return nil, fmt.Errorf("CEE0:0004 是 $%04X，不是 $%04X——這顆 ROM 不是驅動要找的音源 BIOS",
			got, Vector)
	}
	b := &BIOS{m: m, prev: m.CPU.IntHook, Unhandled: map[byte]int{}, Real: true}
	b.m.CPU.IntHook = b.handleReal
	return b, nil
}

// handleReal 只接 TSR。`INT D2h` **只記錄不攔截**——回 false 之後 CPU 照樣
// 跳進 ROM，所以參數看得到、行為仍然是原版韌體的。
func (b *BIOS) handleReal(c *cpu.CPU, n uint8) bool {
	if n == Vector {
		b.record(c)
		return false
	}
	if n == 0x21 && uint8(c.R[cpu.AX]>>8) == 0x31 {
		b.Resident = true
		c.Halted = true
		return true
	}
	if b.prev != nil {
		return b.prev(c, n)
	}
	return false
}

// record 記下一次呼叫的參數，不改變任何狀態機以外的東西。
func (b *BIOS) record(c *cpu.CPU) {
	call := Call{
		AH: uint8(c.R[cpu.AX] >> 8), AL: uint8(c.R[cpu.AX]),
		ES: c.Seg[cpu.ES], BX: c.R[cpu.BX], Step: b.m.Steps,
	}
	b.Calls = append(b.Calls, call)
	switch call.AH {
	case CmdInitialize:
		b.WorkSegment = call.ES
	case CmdPlay:
		b.PlayDataSegment, b.PlayDataOffset = call.ES, call.BX
		b.Playing = true
	case CmdAllStop:
		b.Playing = false
	case CmdContPlay:
		b.Playing = true
	}
}

// 命令編號。名稱照 NEC《PC-9800 Technical Databook BIOS》的音源 BIOS 章。
const (
	CmdInitialize = 0x00
	CmdPlay       = 0x01
	CmdClear      = 0x02
	CmdAllStop    = 0x19
	CmdContPlay   = 0x1A
)

// Call 是一次 `INT D2h`。
type Call struct {
	AH, AL byte
	ES, BX uint16
	Step   uint64
}

func (c Call) String() string {
	return fmt.Sprintf("AH=%02X AL=%02X ES:BX=%04X:%04X", c.AH, c.AL, c.ES, c.BX)
}

// BIOS 是掛在一台機器上的音源 BIOS 替身。
type BIOS struct {
	// Calls 是所有進來的呼叫，依序。
	Calls []Call
	// Unhandled 統計沒實作的命令各出現幾次。**要印出來。**
	Unhandled map[byte]int

	// WorkSegment 是 INITIALIZE 帶進來的工作區段。
	WorkSegment uint16
	// PlayData 是 PLAY 帶進來的演奏資料結構位址。
	PlayDataSegment, PlayDataOffset uint16
	// Playing 反映最後一次 PLAY／ALLSTOP。
	Playing bool

	// Resident 為真代表驅動已經 TSR，安裝路徑跑完了。
	Resident bool
	// Real 為真代表這是真的 ROM 在跑，不是本層的替身。
	Real bool

	m    *machine.Machine
	prev func(*cpu.CPU, uint8) bool
}

// Install 把音源 BIOS 掛上去。**要在 DOS 服務層 Install 之後叫**，
// 沒接到的中斷會轉給它。
func Install(m *machine.Machine) *BIOS {
	b := &BIOS{m: m, prev: m.CPU.IntHook, Unhandled: map[byte]int{}}
	// 介面表：偏移 4 放向量號碼，偏移 6 放進入點。進入點不會真的被執行
	// （INT D2h 由本層接管），擺著是為了讓驅動的偵測與安裝路徑照原樣跑完。
	base := uint32(InterfaceSegment) * 16
	m.Write16(base+4, Vector)
	m.Write16(base+6, 0x0100)
	m.CPU.IntHook = b.handle
	return b
}

func (b *BIOS) handle(c *cpu.CPU, n uint8) bool {
	if n == Vector {
		b.soundBIOS(c)
		return true
	}
	// TSR：驅動裝完就常駐。沒接的話機器會從 int 21h 之後繼續跑飛。
	if n == 0x21 && uint8(c.R[cpu.AX]>>8) == 0x31 {
		b.Resident = true
		c.Halted = true
		return true
	}
	if b.prev != nil {
		return b.prev(c, n)
	}
	return false
}

func (b *BIOS) soundBIOS(c *cpu.CPU) {
	b.record(c)
	switch c.R[cpu.AX] >> 8 {
	case CmdInitialize, CmdPlay, CmdClear, CmdAllStop, CmdContPlay:
	default:
		b.Unhandled[uint8(c.R[cpu.AX]>>8)]++
	}
}
