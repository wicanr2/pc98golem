package dos

import (
	"github.com/wicanr2/pc98golem/internal/cpu"
	"github.com/wicanr2/pc98golem/internal/machine"
)

// BIOS 服務：`int 10h` 視訊、`int 33h` 滑鼠、`int 16h` 鍵盤。

// int10 是 BIOS 視訊服務（`docs/spec/004` §3）。
//
// `RUN.EXE` 開場會做一整套顯示卡偵測。**回傳值不對就判定環境不支援然後
// 退出，而且退出訊息不走 `int 21h`**，所以主控台攔截收不到
// （`rich2/docs/re/005` §12）。這裡讓它相信自己在一張標準 VGA 上。
func (d *DOS) int10(c *cpu.CPU) {
	fn := ah(c)
	switch fn {
	case 0x00: // 設視訊模式
		// 記進 BDA。一直回 3 的話，程式設了 mode 13h 之後再查會以為沒設成功。
		d.M.SetVideoMode(al(c) & 0x7F)

	case 0x0E: // TTY 輸出
		if al(c) >= 0x20 {
			d.Console = append(d.Console, al(c))
		}

	case 0x0F: // 取目前視訊模式
		mode := d.M.VideoMode()
		cols := uint8(80)
		switch mode {
		case 0x00, 0x01, 0x04, 0x05, 0x0D, 0x13:
			cols = 40
		}
		setAH(c, cols)
		setAL(c, mode)
		setBH(c, 0) // 顯示頁

	case 0x11: // 取字型指標 → ES:BP
		if al(c) == 0x30 {
			c.Seg[cpu.ES] = machine.StubSeg
			c.R[cpu.BP] = 0
			c.R[cpu.CX] = 16 // 每字元掃描線數
			c.R[cpu.DX] = 24 // 螢幕列數 − 1
		}

	case 0x12: // EGA/VGA 替代選擇
		// ⚠ **子功能選擇子在 `BL`，不是 `AL`。**
		// 查 `AL` 的話那個分支永遠不成立（`docs/spec/004` §3）。
		switch bl(c) {
		case 0x10: // 取 EGA 資訊
			setBH(c, 0x00)       // 彩色模式
			setBL(c, 0x03)       // 記憶體 256 KB
			c.R[cpu.CX] = 0x0009 // 功能位元／切換設定
		case 0x20, 0x30, 0x31, 0x32, 0x33, 0x34:
			setAL(c, 0x12) // 表示有支援
		}

	case 0x1A: // 取顯示卡組合碼
		// **BASIC runtime 用這支判斷能不能 `SCREEN 13`。**
		// 沒實作 → `BL` 是垃圾 → runtime 認定不是 VGA → `SCREEN 13` 回
		// Illegal function call，而症狀完全不指向這裡。
		setAL(c, 0x1A) // 表示本服務有支援
		setBL(c, 0x08) // 使用中：VGA 彩色
		setBH(c, 0x00) // 替代：無

	case 0x1B: // 取功能／狀態資訊表 → ES:DI
		table := make([]byte, 64)
		table[0x25] = 0x08 // 顯示卡代碼：VGA 彩色
		table[0x29] = 0x03 // 記憶體大小：256 KB
		d.M.WriteBytes(cpu.Addr(c.Seg[cpu.ES], c.R[cpu.DI]), table)
		setAL(c, 0x1B) // 表示本服務有支援

	case 0x02, 0x03, 0x05, 0x06, 0x09, 0x0A, 0x10:
		// 設游標／取游標／設頁／捲動／寫字元／調色盤：收下就好，
		// 呼叫端不看回傳值。

	default:
		d.note(0x10, fn, al(c))
	}
}

// int33 是滑鼠驅動（`docs/spec/004` §4，出處 `rich2/docs/re/182`）。
//
// **防拷畫面只吃滑鼠**：`rich2/docs/playtest/001` §3 記著鍵盤全都無效。
// 沒有這支的話遊戲偵測不到滑鼠，整個密碼畫面就沒有任何可用輸入——
// 而且不會有錯誤訊息，看起來就只是「卡住」。
func (d *DOS) int33(c *cpu.CPU) {
	m := &d.Mouse
	// **先把功能號存起來**：下面好幾個分支會覆寫 AX，之後再拿 AX 判斷
	// 就是在讀自己剛寫進去的值。（同一個形狀在 CPU 的 `PUSH SP` 上踩過。）
	fn := c.R[cpu.AX]
	if m.Calls != nil {
		m.Calls[fn]++
	}
	switch fn {
	case 0x0000: // 重設並偵測
		c.R[cpu.AX] = 0xFFFF // 已安裝
		c.R[cpu.BX] = 2      // 兩個鍵
		m.Buttons = 0

	case 0x0001, 0x0002: // 顯示／隱藏游標
		// 遊戲**從來不叫 `AX=1`**（全檔 0 個呼叫端），畫面上那隻小手是
		// 它自己畫的，所以這裡不必真的畫游標（`rich2/docs/re/182` §4.1）。

	case 0x0003: // 取位置與鍵狀態
		// **記下每次輪詢回報出去的東西。** 這是分辨「輸入沒送到」與
		// 「送到了但答錯」的唯一辦法——兩者的畫面表現一模一樣。
		m.Polls = append(m.Polls, Poll{X: m.X, Y: m.Y, Buttons: m.Buttons,
			Step: d.M.Steps})
		c.R[cpu.BX] = m.Buttons
		c.R[cpu.CX] = m.X * m.XScale
		c.R[cpu.DX] = m.Y

	case 0x0004: // 設位置
		if m.XScale > 0 {
			m.X = c.R[cpu.CX] / m.XScale
		}
		m.Y = c.R[cpu.DX]
		m.Sets = append(m.Sets, Poll{X: m.X, Y: m.Y, Step: d.M.Steps})

	case 0x0005, 0x0006: // 按下／放開的統計
		c.R[cpu.AX] = m.Buttons
		if fn == 0x0005 {
			c.R[cpu.BX] = m.Press
			m.Press = 0
		} else {
			c.R[cpu.BX] = m.Release
			m.Release = 0
		}
		c.R[cpu.CX] = m.X * m.XScale
		c.R[cpu.DX] = m.Y

	case 0x0007, 0x0008: // 設水平／垂直範圍：收下就好
	default:
		d.note(0x33, uint8(fn>>8), uint8(fn))
	}
}

// int16 是 BIOS 鍵盤。
//
// ⚠ **遊戲的鍵盤輸入不走這條**——它走 `int 21h AH=3Fh` 讀 handle 0
// （BASIC 的 `INKEY$`）。`int 16h` 全程只被呼叫 20–40 次，閒置期間完全沒動
// （`rich2/docs/re/005`「輸入路徑」）。所以這支只要不說謊就好。
func (d *DOS) int16(c *cpu.CPU) {
	switch ah(c) {
	case 0x00, 0x10: // 讀按鍵（阻塞）
		if len(d.Stdin) == 0 {
			c.R[cpu.AX] = 0
			return
		}
		c.R[cpu.AX] = keyWord(d.Stdin[0])
		d.Stdin = d.Stdin[1:]
	case 0x01, 0x11: // 查有沒有按鍵：ZF=1 表示沒有
		if len(d.Stdin) == 0 {
			c.SetFlags(c.Flags | cpu.ZF)
			return
		}
		c.SetFlags(c.Flags &^ cpu.ZF)
		c.R[cpu.AX] = keyWord(d.Stdin[0]) // 查看不取走
	case 0x02, 0x12: // 取旗標狀態
		setAL(c, 0)
	default:
		d.note(0x16, ah(c), al(c))
	}
}

// keyWord 把一個 ASCII 位元組換成 BIOS 的 AX（AH ＝ 掃描碼、AL ＝ ASCII）。
//
// **掃描碼不能省**：只填 AL 的話，凡是用 AH 判鍵的程式都會收到 0，
// 而 0 在很多程式裡是「延伸鍵」的前綴——那會被讀成方向鍵。
// 表只收得下我們真的會送的鍵；沒收錄的回掃描碼 0，並在報告裡記一筆。
func keyWord(b uint8) uint16 {
	if sc, ok := scanCode[b]; ok {
		return uint16(sc)<<8 | uint16(b)
	}
	return uint16(b)
}

// scanCode 是 IBM PC 的 set-1 掃描碼（只列我們送得出去的鍵）。
var scanCode = map[uint8]uint8{
	0x1B: 0x01, // ESC
	'1': 0x02, '2': 0x03, '3': 0x04, '4': 0x05, '5': 0x06,
	'6': 0x07, '7': 0x08, '8': 0x09, '9': 0x0A, '0': 0x0B,
	'\r': 0x1C, '\n': 0x1C, ' ': 0x39,
	'q': 0x10, 'w': 0x11, 'e': 0x12, 'r': 0x13, 't': 0x14,
	'y': 0x15, 'u': 0x16, 'i': 0x17, 'o': 0x18, 'p': 0x19,
	'a': 0x1E, 's': 0x1F, 'd': 0x20, 'f': 0x21, 'g': 0x22,
	'h': 0x23, 'j': 0x24, 'k': 0x25, 'l': 0x26,
	'z': 0x2C, 'x': 0x2D, 'c': 0x2E, 'v': 0x2F, 'b': 0x30,
	'n': 0x31, 'm': 0x32,
	'Y': 0x15, 'N': 0x31,
}

// int13 是 BIOS 磁碟服務。
//
// 遊戲用它做**防拷檢查**（`rich2/docs/playtest/001`：開場要輸入密碼）。
// 這裡一律回成功、狀態 0——真正的防拷邏輯在程式自己那邊。
func (d *DOS) int13(c *cpu.CPU) {
	switch ah(c) {
	case 0x00, 0x04: // 重設磁碟系統／驗證磁區
		setAH(c, 0)
		clearCarry(c)
	case 0x01: // 取上一次的狀態
		setAH(c, 0)
		clearCarry(c)
	default:
		// 讀寫磁區沒實作：**回失敗**，不要假裝成功。
		// 假裝成功的話呼叫端會拿沒填過的緩衝區當資料用。
		d.note(0x13, ah(c), al(c))
		setAH(c, 0x80) // 逾時
		setCarry(c)
	}
}

// int1A 是 BIOS 的系統時鐘服務。
//
// **不實作會卡死**：程式用 `AH=00` 讀 tick 計數當延遲的依據，
// 讀到的值一直不動就永遠等下去（`SANGOKU`／`MAIN.EXE` 開場實測，
// 沒接之前一路輪詢 13,750 次還在原地）。
//
// tick 本身已經在 BDA 的 `0040:006C`（`machine.bumpBDATicks`），
// 這裡只是把它照 BIOS 的介面交出去。
func (d *DOS) int1A(c *cpu.CPU) {
	switch ah(c) {
	case 0x00: // 取 tick 計數 → CX:DX，AL ＝ 跨日旗標
		lo := d.M.Read16(0x0040*16 + 0x6C)
		hi := d.M.Read16(0x0040*16 + 0x6E)
		c.R[cpu.CX] = hi
		c.R[cpu.DX] = lo
		setAL(c, d.M.Read8(0x0040*16+0x70))
		d.M.Write8(0x0040*16+0x70, 0) // 讀過就清，與真機同
	case 0x01: // 設 tick 計數
		d.M.Write16(0x0040*16+0x6C, c.R[cpu.DX])
		d.M.Write16(0x0040*16+0x6E, c.R[cpu.CX])
	case 0x02, 0x04: // 讀 RTC 的時、分、秒／年、月、日
		// 沒有 RTC 就照「沒有時鐘」回：進位旗標立起來。
		// 回一組編出來的時間比較危險——程式可能拿它當亂數種子。
		setCarry(c)
	default:
		d.note(0x1A, ah(c), al(c))
		clearCarry(c)
	}
}
