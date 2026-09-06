package dos

import (
	"os"
	"strings"

	"github.com/wicanr2/pc98golem/internal/cpu"
	"github.com/wicanr2/pc98golem/internal/machine"
)

// EXEC（`int 21h AX=4B00h`）、子程式回傳碼（`AH=4Dh`）、EMS 最小子集
// （`int 67h` AH=40h／42h）與字元裝置 EMMXXXX0。
// 規格：`docs/spec/007`（READY 部分）。證據全部是 GIN3.COM 的反組譯
// bytes 與 probe 軌跡（`~/cht/logh3/docs/re/01`）；只實作有證據的部分。

// execFrame 是一次 EXEC 推進去的父程式狀態。
type execFrame struct {
	regs       cpu.CPU // 完整暫存器組（IP 已指在 int 21h 之後）
	psp        uint16  // 父程式的 PSP（回來時還原 curPSP）
	handleBase uint16  // 子程式結束時關掉 >= 這個值的 handle
	ivt        [3][2]uint16
}

// exec 是 `AX=4B00h`：載入並執行子程式。
//
// **記憶體不做快照還原**——隔離靠 bump 配置器（子程式載在父程式之上），
// 子程式對視訊記憶體／IVT 的寫入保留，這符合真 DOS（畫面不會因程式結束
// 而消失）。代價是子程式踩父程式空間不會被擋（spec §6，有證據再說）。
func (d *DOS) exec(c *cpu.CPU) {
	if al(c) != 0x00 {
		d.note(0x21, 0x4B, al(c))
		c.R[cpu.AX] = 1
		setCarry(c)
		return
	}
	name := d.readCString(c.Seg[cpu.DS], c.R[cpu.DX], 128)
	path := d.resolve(name)
	if path == "" {
		d.Missing = append(d.Missing, name)
		c.R[cpu.AX] = 2 // File not found
		setCarry(c)
		return
	}
	img, err := os.ReadFile(path)
	if err != nil {
		d.Missing = append(d.Missing, name)
		c.R[cpu.AX] = 2
		setCarry(c)
		return
	}
	parags, err := machine.MZImageParags(img)
	if err != nil {
		d.Missing = append(d.Missing, name)
		c.R[cpu.AX] = 8
		setCarry(c)
		return
	}
	// PSP(0x10)＋映像段數，PSP 前再留一段假 MCB（比照 alloc 的 +1）。
	need := uint16(parags) + 0x11
	if avail := uint16(machine.MemTop) - d.freeSeg; need > avail {
		c.R[cpu.AX] = 8 // 記憶體不足
		c.R[cpu.BX] = avail
		setCarry(c)
		return
	}
	psp := d.freeSeg + 1

	// 參數區 ES:BX：+0 env（0＝繼承）、+2/+4 尾巴、+6/+8 FCB1、+A/+C FCB2
	// （bytes 證據：GIN3.COM 0215–023C）。
	pb := cpu.Addr(c.Seg[cpu.ES], c.R[cpu.BX])
	envSeg := d.M.Read16(pb)
	tailOff, tailSeg := d.M.Read16(pb+2), d.M.Read16(pb+4)
	fcb1Off, fcb1Seg := d.M.Read16(pb+6), d.M.Read16(pb+8)
	fcb2Off, fcb2Seg := d.M.Read16(pb+10), d.M.Read16(pb+12)

	f := execFrame{regs: *c, psp: d.curPSP, handleBase: d.nextHandle}
	for i, n := range []uint8{0x22, 0x23, 0x24} { // DOS 保管的三個向量
		f.ivt[i][0] = d.M.Read16(uint32(n) * 4)
		f.ivt[i][1] = d.M.Read16(uint32(n)*4 + 2)
	}
	d.execStack = append(d.execStack, f)
	d.curPSP = psp
	d.freeSeg = psp + need

	if err := d.M.LoadEXEAt(img, psp); err != nil {
		// resolve 過的檔案解析失敗：還原堆疊，報 CF。
		d.execStack = d.execStack[:len(d.execStack)-1]
		d.curPSP = f.psp
		d.freeSeg = psp - 1
		c.R[cpu.AX] = 8
		setCarry(c)
		return
	}

	if envSeg == 0 {
		envSeg = machine.EnvSeg // 繼承父程式環境
	}
	d.M.Write16(uint32(psp)*16+0x2C, envSeg)

	// 尾巴（count＋文字＋CR）抄到 PSP+80h，上限 127 bytes。
	if tailSeg != 0 || tailOff != 0 {
		src := cpu.Addr(tailSeg, tailOff)
		n := int(d.M.Read8(src))
		if n > 126 {
			n = 126
		}
		buf := make([]byte, n+2)
		buf[0] = uint8(n)
		for i := 0; i < n+1; i++ { // 含結尾 CR
			buf[i+1] = d.M.Read8(src + 1 + uint32(i))
		}
		d.M.WriteBytes(uint32(psp)*16+0x80, buf)
	}
	// FCB 原樣各抄 16 bytes（簡化，spec §6）。
	for i := uint32(0); i < 16; i++ {
		d.M.Write8(uint32(psp)*16+0x5C+i, d.M.Read8(cpu.Addr(fcb1Seg, fcb1Off)+i))
		d.M.Write8(uint32(psp)*16+0x6C+i, d.M.Read8(cpu.Addr(fcb2Seg, fcb2Off)+i))
	}
	d.Opened = append(d.Opened, name)
}

// childExit 是子程式結束（EXEC 深度 > 0 時的 AH=4Ch／AH=00h／int 20h）：
// 記回傳碼、關子程式開的 handle、還原 IVT 22h–24h、彈回父程式暫存器。
func (d *DOS) childExit(c *cpu.CPU, code uint8) {
	d.lastExit, d.lastTerm = code, 0 // 0 ＝ 正常結束
	f := d.execStack[len(d.execStack)-1]
	d.execStack = d.execStack[:len(d.execStack)-1]
	for h, hh := range d.handles {
		if h >= f.handleBase {
			if hh.f != nil {
				hh.f.Close()
			}
			delete(d.handles, h)
		}
	}
	for i, n := range []uint8{0x22, 0x23, 0x24} {
		d.M.Write16(uint32(n)*4, f.ivt[i][0])
		d.M.Write16(uint32(n)*4+2, f.ivt[i][1])
	}
	f.regs.SetFlags(f.regs.Flags &^ cpu.CF) // EXEC 成功
	*c = f.regs
	d.curPSP = f.psp
}

// getReturnCode 是 `AH=4Dh`：AL ＝ 子程式結束碼、AH ＝ 結束方式（0 ＝ 正常）。
//
// ⚠ **AH 一定要清 0**——GIN3.COM 拿整個 AX 比較（`or ax,ax`／`cmp ax,1`），
// AH 留垃圾會讓「回碼 0」讀成非 0。
func (d *DOS) getReturnCode(c *cpu.CPU) {
	c.R[cpu.AX] = uint16(d.lastExit)
	clearCarry(c)
}

// int67 是 EMS 最小子集（spec §4）：只有查詢，沒有頁框映射。
// 本機宣稱 8／8 頁——恰好滿足 launcher 的 `cmp bx,8` 探測下限。
func (d *DOS) int67(c *cpu.CPU) {
	switch ah(c) {
	case 0x40: // 取狀態
		setAH(c, 0)
	case 0x42: // 取頁數：BX ＝ 可用、DX ＝ 總計
		setAH(c, 0)
		c.R[cpu.BX] = 8
		c.R[cpu.DX] = 8
	default:
		d.note(0x67, ah(c), al(c))
		setAH(c, 0x84) // 未定義功能
	}
}

// isEMMDevice 回 basename 是不是 EMM 驅動的字元裝置名。
// 開啟它成功 ＝ EMS 驅動存在（GIN3.COM 01D9–01F0 就是這樣偵測的）。
func isEMMDevice(name string) bool {
	base := name
	for i := len(base) - 1; i >= 0; i-- {
		if base[i] == '/' || base[i] == '\\' || base[i] == ':' {
			base = base[i+1:]
			break
		}
	}
	return strings.EqualFold(base, "EMMXXXX0")
}
