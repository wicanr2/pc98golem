// Package cpu 是 Intel 8086／8088 real mode 的整數指令核心。
//
// 規格與驗收判準在 `docs/spec/002-cpu-8086.md`（READY）。範圍刻意窄：
// 沒有 x87、沒有 186 以上、沒有保護模式、不做匯流排週期精確——
// 因為要跑的那一個 binary 用不到（`docs/spec/001` §3）。
package cpu

import "fmt"

// Bus 是 CPU 對外的唯一介面。位址是 20 位元線性位址；
// 8086 的段位移相加會 wrap 到 1 MB 以內，wrap 由 CPU 這邊做完再交出來。
type Bus interface {
	Read8(addr uint32) uint8
	Write8(addr uint32, v uint8)
	In8(port uint16) uint8
	Out8(port uint16, v uint8)
}

// 通用暫存器索引。**照 8086 的編碼順序**，不是照人念的順序——
// ModRM 的 3 位元欄位就是這個索引，照抄比翻譯不容易錯（`docs/spec/002` §2）。
const (
	AX = iota
	CX
	DX
	BX
	SP
	BP
	SI
	DI
)

// 段暫存器索引，同樣照編碼順序。
const (
	ES = iota
	CS
	SS
	DS
)

// 旗標位元（`docs/spec/002` §3）。
const (
	CF = 1 << 0
	PF = 1 << 2
	AF = 1 << 4
	ZF = 1 << 6
	SF = 1 << 7
	TF = 1 << 8
	IF = 1 << 9
	DF = 1 << 10
	OF = 1 << 11
)

// flagsMask／flagsSet 是 8086 固定的保留位元：bit 1 恆為 1、bit 3／5 恆為 0、
// bit 12–15 恆為 1。**每一個寫入旗標的地方都要過 setFlags**——
// 少套一處，SingleStepTests 會整批紅，而且錯的是「看起來對的值」。
const (
	flagsMask = 0x0FD5
	flagsSet  = 0xF002
)

// 前綴的段覆寫狀態。noSegOverride 用 −1 以外的值是為了讓零值有意義。
const noSegOverride = -1

// Model 是要模擬的機型。**只影響 `60`–`6F` 這一段**（`docs/spec/002` §1.1）。
type Model uint8

const (
	// Model8086 是預設：`60`–`6F` 是 `70`–`7F` 條件跳躍的別名。
	// 那是 SingleStepTests 語料驗證過的實機行為，驗收一律用這個。
	Model8086 Model = iota

	// Model80186 讓 `68` ＝ `PUSH imm16`、`6A` ＝ `PUSH imm8`，
	// 其餘 `60`–`6F` 報「未實作的 opcode」。
	//
	// `RUN_full.EXE` 需要它：主程式區有 3,345 個 `PUSH imm`，
	// 用 8086 的別名解讀會讓指令長度差一個 byte，然後整串錯位——
	// **而且不會報錯**（`docs/spec/002` §1.1 那個方框）。
	Model80186
)

// CPU 是一顆 8086（或 80186，看 Model）。零值不可用，用 New。
type CPU struct {
	R     [8]uint16
	Seg   [4]uint16
	IP    uint16
	Flags uint16
	Bus   Bus

	// Model 決定 `60`–`6F` 怎麼解。預設是 Model8086。
	Model Model

	// IntHook 讓上層攔截 INT。回傳 true 表示「我處理完了」，
	// CPU 就不走真正的向量表——DOS 服務層靠它接管 int 21h 那些。
	//
	// **回傳 false 時 CPU 會照常跳向量**，所以沒實作的服務不會安靜地變成 nop。
	IntHook func(c *CPU, n uint8) bool

	// Halted 由 HLT 設起來；外層要靠中斷把它清掉。
	Halted bool

	// 前綴狀態，每道指令開頭重設。
	segOverride int
	repPrefix   uint8 // 0 ＝ 沒有；0xF2 ＝ REPNE；0xF3 ＝ REP／REPE
	lock        bool

	// 本道指令的起點，除法例外與中斷要用（`docs/spec/002` §4 第 3 點）。
	opCS, opIP uint16
}

// New 造一顆接在 bus 上的 CPU，暫存器全 0、旗標是 8086 的重置值。
func New(bus Bus) *CPU {
	c := &CPU{Bus: bus}
	c.Reset()
	return c
}

// Reset 把 CPU 拉回 8086 的重置狀態：CS:IP = F000:FFF0，其餘為 0。
func (c *CPU) Reset() {
	c.R = [8]uint16{}
	c.Seg = [4]uint16{}
	c.Seg[CS] = 0xFFFF
	c.IP = 0
	c.SetFlags(0)
	c.Halted = false
}

// SetFlags 寫旗標並套上 8086 的固定位元。
func (c *CPU) SetFlags(v uint16) { c.Flags = (v & flagsMask) | flagsSet }

// Flag 回報某一個旗標開著沒有。
func (c *CPU) Flag(f uint16) bool { return c.Flags&f != 0 }

// setFlag 設或清一個旗標。
func (c *CPU) setFlag(f uint16, on bool) {
	if on {
		c.Flags |= f
	} else {
		c.Flags &^= f
	}
}

// 8 位元暫存器存取。索引順序是 AL CL DL BL AH CH DH BH：
// 低 4 個是 R[i] 的低位元組、高 4 個是 R[i-4] 的高位元組。
func (c *CPU) reg8(i int) uint8 {
	if i < 4 {
		return uint8(c.R[i])
	}
	return uint8(c.R[i-4] >> 8)
}

func (c *CPU) setReg8(i int, v uint8) {
	if i < 4 {
		c.R[i] = c.R[i]&0xFF00 | uint16(v)
		return
	}
	c.R[i-4] = c.R[i-4]&0x00FF | uint16(v)<<8
}

// Addr 把 段:位移 換成 20 位元線性位址。**8086 只有 20 條位址線**，
// 所以 FFFF:0010 會 wrap 回 0——這是真的行為，不是溢位 bug。
func Addr(seg, off uint16) uint32 {
	return (uint32(seg)<<4 + uint32(off)) & 0xFFFFF
}

func (c *CPU) read8(seg, off uint16) uint8     { return c.Bus.Read8(Addr(seg, off)) }
func (c *CPU) write8(seg, off uint16, v uint8) { c.Bus.Write8(Addr(seg, off), v) }

// 16 位元存取是兩次 8 位元，而且**位移各自 wrap**：
// 讀 DS:FFFF 的 word 會拿到 DS:FFFF 與 DS:0000，不是跨到下一段。
func (c *CPU) read16(seg, off uint16) uint16 {
	lo := uint16(c.read8(seg, off))
	hi := uint16(c.read8(seg, off+1))
	return lo | hi<<8
}

func (c *CPU) write16(seg, off uint16, v uint16) {
	c.write8(seg, off, uint8(v))
	c.write8(seg, off+1, uint8(v>>8))
}

// fetch8／fetch16 從 CS:IP 取指令位元組並前進 IP。
func (c *CPU) fetch8() uint8 {
	v := c.read8(c.Seg[CS], c.IP)
	c.IP++
	return v
}

func (c *CPU) fetch16() uint16 {
	lo := uint16(c.fetch8())
	hi := uint16(c.fetch8())
	return lo | hi<<8
}

// dataSeg 回報這道指令該用哪個段：有段前綴就用它，否則用 def。
func (c *CPU) dataSeg(def int) uint16 {
	if c.segOverride != noSegOverride {
		return c.Seg[c.segOverride]
	}
	return c.Seg[def]
}

func (c *CPU) push(v uint16) {
	c.R[SP] -= 2
	c.write16(c.Seg[SS], c.R[SP], v)
}

func (c *CPU) pop() uint16 {
	v := c.read16(c.Seg[SS], c.R[SP])
	c.R[SP] += 2
	return v
}

// Interrupt 走真正的向量表：推旗標與返回位址、清 IF／TF、跳 0000:(n*4)。
func (c *CPU) Interrupt(n uint8) {
	c.push(c.Flags)
	c.setFlag(IF, false)
	c.setFlag(TF, false)
	c.push(c.Seg[CS])
	c.push(c.IP)
	off := uint16(n) * 4
	c.IP = c.read16(0, off)
	c.Seg[CS] = c.read16(0, off+2)
	c.Halted = false
}

// Rewind 把 CS:IP 退回這一道指令的起點（含前綴）。
//
// 用途只有一個：**讓一道 `INT` 重新執行**。真實 DOS 的 `AH=3Fh` 讀 stdin
// 會擋住，計時器 ISR 在背景推進動畫；模擬器立刻回「讀到 0 個」的話，
// 主程式會當成輸入結束然後退出——**動畫一格都沒播**
// （`rich2/docs/re/012` §7）。把 IP 退回去再讓中斷注入，語意才完整。
//
// 退的是 opCS:opIP 而不是 `IP − 2`，因為前綴會讓指令長度不是 2。
func (c *CPU) Rewind() { c.Seg[CS], c.IP = c.opCS, c.opIP }

// Error 是執行不下去時回傳的原因。CPU 自己不 panic——
// 上層要能把「跑到沒實作的東西」與「程式自己出錯」分開記錄。
type Error struct {
	CS, IP uint16
	Op     uint8
	Reason string
}

func (e *Error) Error() string {
	return fmt.Sprintf("cpu: %04X:%04X op %02X：%s", e.CS, e.IP, e.Op, e.Reason)
}

func (c *CPU) errf(op uint8, format string, a ...any) *Error {
	return &Error{CS: c.opCS, IP: c.opIP, Op: op, Reason: fmt.Sprintf(format, a...)}
}
