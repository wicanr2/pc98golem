package machine

import (
	"encoding/binary"
	"fmt"

	"github.com/wicanr2/pc98golem/internal/cpu"
)

// MZ 載入器、PSP、環境區塊與 MCB 鏈（`docs/spec/003` §3）。
//
// **輸入是已經解包的映像。** `RUN.EXE` 是雙層打包
// `LZEXE93(EXEPACK(本體))`，兩層都要先解（`rich2/CLAUDE.md` §4.1）；
// 解包工具在 `rich2/tools/`，產物由呼叫端給進來。做在這裡等於每次跑都
// 重做一遍，而且會讓「載入器對不對」與「解包對不對」兩個問題混在一起。

// mzHeader 是 MZ 檔頭裡我們用得到的欄位。
type mzHeader struct {
	LastPage  uint16 // 最後一頁用了幾個 byte（0 表示整頁）
	Pages     uint16 // 512 byte 的頁數
	Relocs    uint16 // 重定位項數
	HeaderPar uint16 // 檔頭佔幾個段
	SS, SP    uint16
	IP, CS    uint16
	RelocOff  uint16
}

func parseMZ(data []byte) (*mzHeader, error) {
	if len(data) < 28 {
		return nil, fmt.Errorf("machine: 檔案只有 %d bytes，放不下 MZ 檔頭", len(data))
	}
	if data[0] != 'M' || data[1] != 'Z' {
		return nil, fmt.Errorf("machine: 不是 MZ 執行檔（開頭是 %02X %02X）", data[0], data[1])
	}
	u := func(off int) uint16 { return binary.LittleEndian.Uint16(data[off:]) }
	return &mzHeader{
		LastPage: u(2), Pages: u(4), Relocs: u(6), HeaderPar: u(8),
		SS: u(14), SP: u(16), IP: u(20), CS: u(22), RelocOff: u(24),
	}, nil
}

// mzImage 解出 MZ 映像並把重定位套到指定的載入段。
func mzImage(data []byte, loadSeg uint16) ([]byte, *mzHeader, error) {
	h, err := parseMZ(data)
	if err != nil {
		return nil, nil, err
	}
	hdr := int(h.HeaderPar) * 16
	total := (int(h.Pages)-1)*512 + int(h.LastPage)
	if h.LastPage == 0 {
		total = int(h.Pages) * 512
	}
	if hdr > len(data) || total > len(data) || total <= hdr {
		return nil, nil, fmt.Errorf("machine: MZ 檔頭說映像是 %d..%d，但檔案只有 %d bytes",
			hdr, total, len(data))
	}
	image := append([]byte(nil), data[hdr:total]...)

	// **重定位一定要套。** 檔案裡的遠指標段值是「相對載入段」的，
	// 載入器要加上實際載入段；沒套的話第一個 far call 就飛到錯的地方。
	applied := 0
	for i := 0; i < int(h.Relocs); i++ {
		p := int(h.RelocOff) + i*4
		if p+4 > len(data) {
			return nil, nil, fmt.Errorf("machine: 重定位表第 %d 筆超出檔案", i)
		}
		off := binary.LittleEndian.Uint16(data[p:])
		seg := binary.LittleEndian.Uint16(data[p+2:])
		idx := int(seg)*16 + int(off)
		if idx+2 > len(image) {
			continue // 指到映像外，跳過；不是錯誤，舊 linker 會產生這種項
		}
		v := binary.LittleEndian.Uint16(image[idx:])
		binary.LittleEndian.PutUint16(image[idx:], v+loadSeg)
		applied++
	}
	if int(h.Relocs) > 0 && applied == 0 {
		return nil, nil, fmt.Errorf("machine: 有 %d 筆重定位卻一筆都沒套上——映像可能被截斷",
			h.Relocs)
	}
	return image, h, nil
}

// MZImageParags 回 MZ 映像（不含檔頭）佔幾個段。EXEC 配置記憶體用
// （`docs/spec/007` §2）。
func MZImageParags(data []byte) (int, error) {
	h, err := parseMZ(data)
	if err != nil {
		return 0, err
	}
	hdr := int(h.HeaderPar) * 16
	total := (int(h.Pages)-1)*512 + int(h.LastPage)
	if h.LastPage == 0 {
		total = int(h.Pages) * 512
	}
	if hdr > len(data) || total > len(data) || total <= hdr {
		return 0, fmt.Errorf("machine: MZ 檔頭說映像是 %d..%d，但檔案只有 %d bytes",
			hdr, total, len(data))
	}
	return (total - hdr + 15) / 16, nil
}

// LoadEXE 把一個已經解包的 MZ 映像載進機器並把 CPU 設到進入點。
func (m *Machine) LoadEXE(data []byte) error {
	image, h, err := mzImage(data, LoadSeg)
	if err != nil {
		return err
	}
	m.WriteBytes(LoadSeg*16, image)
	m.ImageBase, m.ImageLen = LoadSeg*16, len(image)
	// 映像之後才是可配置區。BASIC runtime 會先要一大塊當堆積，
	// 給不夠就報 Error 07（Out of memory）。
	m.FreeSeg = LoadSeg + uint16((len(image)+15)/16) + 1

	m.initPSP()
	m.initMCB()

	m.setEntry(LoadSeg, h, PSPSeg)
	return nil
}

// LoadEXEAt 把 MZ 映像載到指定的 PSP 段（EXEC 用，`docs/spec/007` §2）。
//
// 與 LoadEXE 的差別：**不動 ImageBase／FreeSeg，也不重建全域 MCB 鏈**——
// 記憶體配置是 dos 層的 bump 配置器管的。PSP 前一段補一個假 MCB
// （擁有者 ＝ 子程式 PSP），讓子程式的 `AH=4Ah` 看得到自洽的東西。
func (m *Machine) LoadEXEAt(data []byte, pspSeg uint16) error {
	loadSeg := pspSeg + 0x10
	image, h, err := mzImage(data, loadSeg)
	if err != nil {
		return err
	}
	m.WriteBytes(uint32(loadSeg)*16, image)
	m.initPSPAt(pspSeg)
	m.writeMCB(pspSeg, uint16((len(image)+15)/16)+0x11)
	m.setEntry(loadSeg, h, pspSeg)
	// EXEC 進入的通用暫存器清 0（簡化，`docs/spec/007` §6）；
	// SS:SP 已由 setEntry 設好。
	for i := range m.CPU.R {
		if i != cpu.SP {
			m.CPU.R[i] = 0
		}
	}
	return nil
}

// setEntry 把 CPU 設到 MZ 的進入點。
func (m *Machine) setEntry(loadSeg uint16, h *mzHeader, pspSeg uint16) {
	c := m.CPU
	c.Seg[cpu.CS] = loadSeg + h.CS
	c.IP = h.IP
	c.Seg[cpu.SS] = loadSeg + h.SS
	c.R[cpu.SP] = h.SP
	c.Seg[cpu.DS] = pspSeg
	c.Seg[cpu.ES] = pspSeg
}

// writeMCB 在 pspSeg−1 寫一個假 MCB（擁有者 ＝ pspSeg）。
func (m *Machine) writeMCB(pspSeg, paras uint16) {
	mcb := uint32((pspSeg - 1) * 16)
	m.Mem[mcb] = 'M'
	m.Write16(mcb+1, pspSeg)
	m.Write16(mcb+3, paras)
	m.WriteBytes(mcb+8, []byte("        "))
}

// LoadCOM 載入 .COM 映像：無檔頭、無重定位，整份檔案放在 PSP+100h，
// 四個段暫存器都指向 PSP 段，IP ＝ 100h，SP 指向段頂並壓一個 0
// （RET 回 PSP:0 的 int 20h）。
//
// LoadSeg ＝ PSPSeg+10h，所以「PSP+100h」與 MZ 映像的位置是同一個位址，
// MCB 與 FreeSeg 的計算可以直接沿用。
func (m *Machine) LoadCOM(data []byte) error {
	if len(data) == 0 || len(data) > 0xFF00 {
		return fmt.Errorf("machine: COM 映像大小 %d 不合法（1..65280）", len(data))
	}
	m.WriteBytes(LoadSeg*16, data)
	m.ImageBase, m.ImageLen = LoadSeg*16, len(data)
	m.FreeSeg = LoadSeg + uint16((len(data)+15)/16) + 1

	m.initPSP()
	m.initMCB()

	c := m.CPU
	c.Seg[cpu.CS] = PSPSeg
	c.Seg[cpu.DS] = PSPSeg
	c.Seg[cpu.ES] = PSPSeg
	c.Seg[cpu.SS] = PSPSeg
	c.IP = 0x100
	c.R[cpu.SP] = 0xFFFE
	m.Write16(cpu.Addr(PSPSeg, 0xFFFE), 0)
	return nil
}

// initPSP 建一個夠用的 PSP。
//
// 「夠用」的定義是 Microsoft C runtime 啟動不炸——每一欄都有一個
// 具體的呼叫端，不是照手冊填滿。
func (m *Machine) initPSP() { m.initPSPAt(PSPSeg) }

// initPSPAt 在指定段建 PSP。父行程欄位與環境段維持指到全域的
// PSPSeg／EnvSeg——EXEC 的子程式因此繼承父程式的環境（`docs/spec/007` §2）。
func (m *Machine) initPSPAt(pspSeg uint16) {
	psp := uint32(pspSeg) * 16
	m.WriteBytes(psp, []byte{0xCD, 0x20}) // int 20h
	m.Write16(psp+2, MemTop)              // 記憶體上限

	// PSP+16h 是父行程的 PSP。
	m.Write16(psp+0x16, PSPSeg)

	// **PSP+2Ch 是環境區塊的段位址。** Microsoft C runtime 啟動時會去讀它
	// （`__setenvp`），指到 0 會讓後續的 heap 初始化判定失敗。
	// 內容是「空環境 ＋ 計數 1 ＋ 程式路徑」。
	env := uint32(EnvSeg * 16)
	m.WriteBytes(env, append([]byte{0x00, 0x01, 0x00}, append(
		[]byte(`C:\RICH2\RUN.EXE`), 0x00)...))
	m.Write16(psp+0x2C, EnvSeg)

	// PSP+32h／34h 是檔案表大小與位址。
	m.Write16(psp+0x32, 20)
	m.Write16(psp+0x34, 0x18)
	m.Write16(psp+0x36, pspSeg)
}

// initMCB 造一條最小的合法記憶體控制區塊鏈。
//
// ⚠ **這一段的必要性沒有獨立證明。** `rich2/docs/re/005` §4 的
// `DOS memory-arena error` 看起來像 MCB 鏈驗證失敗，但 §10 找到的根因是
// `int 21h AH=4Ah` 的探測語意（`docs/spec/004` §1.2）——連續三輪調 MCB
// 佈局都無效。
//
// 這裡照樣建，理由只有一個：**它是那份實際跑通的實作留下來的**，
// 而現在還沒有「拿掉也照跑」的收據。等 MVP-B 過了再回來拿掉試一次，
// 過得了就刪掉這一段。
//
// 關鍵是鏈上要有一個「擁有者是本程式 PSP」的區塊，而且要涵蓋程式本身，
// 所以第一個 MCB 放在 `PSPSeg − 1`，緊接著就是 PSP。
func (m *Machine) initMCB() {
	const progSize = 0x2000 // 段數，足以蓋住整個程式

	first := uint32((PSPSeg - 1) * 16)
	m.Mem[first] = 'M'
	m.Write16(first+1, PSPSeg)
	m.Write16(first+3, progSize)
	m.WriteBytes(first+8, []byte("        "))

	last := uint32((PSPSeg + progSize) * 16)
	m.Mem[last] = 'Z'
	m.Write16(last+1, 0)
	m.Write16(last+3, 0x0100)
	m.WriteBytes(last+8, []byte("        "))

	// DOS 的「list of lists」：`[BX-2]` 是第一個 MCB 的段位址。
	m.Write16(LOLSeg*16+0x0E, PSPSeg-1)
}
