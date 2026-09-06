package dos

import (
	"github.com/wicanr2/pc98golem/internal/cpu"
	"github.com/wicanr2/pc98golem/internal/machine"
)

// `int 21h`（`docs/spec/004` §2）。
//
// 這張表是**跑出來的**，不是照手冊列的：`rich2/docs/re/005` 那一輪把
// `RUN.EXE` 跑到資產全部載完（`DATA.PAK`／`PART1.PAK`／`SAVE_7.DSK`／
// `RICHA.RIX` 都開了）。沒列到的功能號照原則 3 記一筆，不要靜靜地放行。

func (d *DOS) int21(c *cpu.CPU) {
	fn := ah(c)
	switch fn {
	case 0x00, 0x4C:
		d.exit(c, al(c))

	case 0x02, 0x06:
		d.conOut(c, fn)

	case 0x09: // 輸出 $ 結尾的字串
		addr := cpu.Addr(c.Seg[cpu.DS], c.R[cpu.DX])
		for i := 0; i < 1024; i++ {
			ch := d.M.Read8(addr + uint32(i))
			if ch == '$' {
				break
			}
			d.Console = append(d.Console, ch)
		}
		clearCarry(c)

	case 0x19: // 取目前磁碟機
		// 不實作的話 AL 是垃圾，遊戲把它拼進路徑就變成 `A:\…`，
		// 而 open 還是會成功（我們按檔名解析），**錯誤完全不顯現**。
		setAL(c, d.Drive)

	case 0x1A: // 設 DTA。收下就好，我們不用 FCB 那一套。
		clearCarry(c)

	case 0x1C: // 取指定磁碟機的配置資訊
		// AL ＝ 每叢集磁區數、CX ＝ 磁區大小、DX ＝ 叢集數、DS:BX → 媒體識別碼。
		// 給一組自洽的 1.2 MB 軟碟數字：程式拿它算「還有多少空間可以存檔」，
		// 回 0 會被讀成「磁碟滿了」。媒體識別碼指到樁段的一個位元組。
		setAL(c, 1)
		c.R[cpu.CX] = 512
		c.R[cpu.DX] = 2400
		c.Seg[cpu.DS] = machine.StubSeg
		c.R[cpu.BX] = 0
		d.M.Write8(uint32(machine.StubSeg)*16, 0xF9) // 1.2 MB 軟碟
		clearCarry(c)

	case 0x25: // 設中斷向量 ← DS:DX
		// **一定要真的寫進去。** 程式用它裝自己的 8087 模擬處理常式
		// （`INT 34h`–`3Dh`）；不寫的話所有浮點運算都落空，
		// 而 BASIC 的金錢運算全靠浮點。
		d.M.Write16(uint32(al(c))*4, c.R[cpu.DX])
		d.M.Write16(uint32(al(c))*4+2, c.Seg[cpu.DS])
		clearCarry(c)

	case 0x2A: // 取系統日期 → CX:DH:DL
		c.R[cpu.CX] = 1993
		c.R[cpu.DX] = 1<<8 | 1
		setAL(c, 5) // 星期五
		clearCarry(c)

	case 0x2C: // 取系統時間 → CH:CL:DH:DL
		c.R[cpu.CX] = uint16(d.Now.Hour)<<8 | uint16(d.Now.Min)
		c.R[cpu.DX] = uint16(d.Now.Sec)<<8 | uint16(d.Now.Hundredth)
		clearCarry(c)

	case 0x30: // 取 DOS 版本
		c.R[cpu.AX] = 0x0005 // 5.0
		clearCarry(c)

	case 0x33: // Ctrl-Break 檢查旗標
		c.R[cpu.DX] = 0
		clearCarry(c)

	case 0x35: // 取中斷向量 → ES:BX
		c.R[cpu.BX] = d.M.Read16(uint32(al(c)) * 4)
		c.Seg[cpu.ES] = d.M.Read16(uint32(al(c))*4 + 2)
		clearCarry(c)

	case 0x36: // 取磁碟剩餘空間
		c.R[cpu.AX] = 8     // 每叢集磁區數
		c.R[cpu.BX] = 20000 // 可用叢集
		c.R[cpu.CX] = 512   // 每磁區位元組
		c.R[cpu.DX] = 40000 // 總叢集
		clearCarry(c)

	case 0x3D:
		d.open(c)
	case 0x3E:
		d.close(c)
	case 0x3F:
		d.read(c)
	case 0x40:
		d.write(c)
	case 0x42:
		d.seek(c)

	case 0x44: // IOCTL
		if al(c) == 0x00 { // 取裝置資訊：bit7 = 0 表示是檔案
			c.R[cpu.DX] = uint16(d.Drive)
		}
		c.R[cpu.AX] = 0
		clearCarry(c)

	case 0x47: // 取目前目錄 → DS:SI（64 bytes）
		addr := cpu.Addr(c.Seg[cpu.DS], c.R[cpu.SI])
		buf := []byte(d.Dir)
		if len(buf) > 63 {
			buf = buf[:63]
		}
		d.M.WriteBytes(addr, append(buf, 0))
		c.R[cpu.AX] = 0x0100
		clearCarry(c)

	case 0x48:
		d.alloc(c)
	case 0x49: // 釋放記憶體：收下就好，不做回收
		clearCarry(c)
	case 0x4A:
		d.setBlock(c)
	case 0x4B:
		d.exec(c)
	case 0x4D:
		d.getReturnCode(c)

	case 0x52: // 取 DOS 內部結構表（list of lists）→ ES:BX
		c.Seg[cpu.ES] = machine.LOLSeg
		c.R[cpu.BX] = 0x10
		clearCarry(c)

	default:
		// 原則 1：**不要動 AX**。一開始寫 AX=0 會把「設中斷向量」迴圈的
		// 計數清掉，`AH` 變成 0 就被當成「結束程式」——程式因此提早死掉。
		d.note(0x21, fn, al(c))
		clearCarry(c)
	}
}

// conOut 是 `AH=02h`／`AH=06h` 的主控台輸出。
//
// ⚠ **字元在 `DL`，不是 `AL`。** 第一版對 `AH=06h` 讀了 `AL`，收到的全是
// 垃圾，害 `RUN.EXE` 的錯誤訊息整個漏掉——印字元的路徑是
// `mov dx,ax / mov ah,6 / int 21h`。
func (d *DOS) conOut(c *cpu.CPU, fn uint8) {
	ch := uint8(c.R[cpu.DX])
	if fn == 0x06 && ch == 0xFF {
		// `DL=0FFh` 是「直接主控台**輸入**」，不是輸出。
		// 沒有按鍵時要回 ZF=1。
		setAL(c, 0)
		c.SetFlags(c.Flags | cpu.ZF)
		return
	}
	d.Console = append(d.Console, ch)
	clearCarry(c)
}

// setBlock 是 `AH=4Ah`，**記憶體探測**（`docs/spec/004` §1.2）。
//
//	56C4  bx = 0FFFFh    ; 故意要求 0FFFFh 段
//	56C7  ah = 4Ah
//	56C9  int 21h
//	56CC  jae 錯誤       ; ★ 成功就跳「錯誤」
//	56CE  ah = 4Ah       ; 用 DOS 在 BX 回的實際大小再要一次
//	56D3  jb  錯誤       ; 這次失敗才算錯誤
//
// 一律清 CF 報成功的話**第一次呼叫就掉進錯誤路徑**——那是
// `DOS memory-arena error` 的真正根因，連續三輪調 MCB 佈局都無效。
func (d *DOS) setBlock(c *cpu.CPU) {
	want := c.R[cpu.BX]
	blk := c.Seg[cpu.ES]
	avail := uint16(machine.MemTop) - blk
	if want > avail {
		c.R[cpu.BX] = avail
		c.R[cpu.AX] = 8 // 記憶體不足
		setCarry(c)
		return
	}
	// 程式縮小自己的區塊之後，後面那塊才是可配置的空間。
	// curPSP 是目前最內層的程式——EXEC 進去的子程式縮的是自己
	// （`docs/spec/007` §2）。
	if blk == d.curPSP && blk+want+1 > d.freeSeg {
		d.freeSeg = blk + want + 1
	}
	clearCarry(c)
}

// alloc 是 `AH=48h`：一個真的 bump 配置器。
//
// 第一版固定回 64 KB，`RUN.EXE` 的 BASIC runtime 因此報 Error 07
// （Out of memory）。
func (d *DOS) alloc(c *cpu.CPU) {
	want := c.R[cpu.BX]
	avail := uint16(machine.MemTop) - d.freeSeg
	if want > avail {
		c.R[cpu.AX] = 8
		c.R[cpu.BX] = avail
		setCarry(c)
		return
	}
	seg := d.freeSeg + 1 // +1 給假的 MCB
	d.freeSeg = seg + want
	c.R[cpu.AX] = seg
	clearCarry(c)
}
