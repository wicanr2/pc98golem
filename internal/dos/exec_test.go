package dos

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wicanr2/pc98golem/internal/cpu"
	"github.com/wicanr2/pc98golem/internal/machine"
)

// EXEC／EMS 的測試（`docs/spec/007` §7）。子程式是臨時造的最小 MZ，
// 不需要任何原版檔。

// buildChildMZ 造一支「印 CHILD$ 然後 exit 42」的最小 MZ。
func buildChildMZ() []byte {
	code := []byte{
		0x0E,             // push cs
		0x1F,             // pop ds（DS 進場時指 PSP，不是程式碼段）
		0xBA, 0x0E, 0x00, // mov dx, 14
		0xB4, 0x09, // mov ah, 9
		0xCD, 0x21, // int 21h
		0xB8, 0x2A, 0x4C, // mov ax, 4C2Ah
		0xCD, 0x21, // int 21h
	}
	code = append(code, []byte("CHILD$")...)
	hdr := make([]byte, 32)
	hdr[0], hdr[1] = 'M', 'Z'
	total := len(hdr) + len(code)
	hdr[2] = byte(total % 512)      // LastPage
	hdr[4] = byte(total/512 + 1)    // Pages
	hdr[8] = byte(len(hdr) / 16)    // HeaderPar
	hdr[14], hdr[15] = 0, 0         // SS ＝ 載入段
	hdr[16], hdr[17] = 0x00, 0x01   // SP ＝ 0x0100
	return append(hdr, code...)
}

// TestExecRunsChildAndReturnsToParent 釘住 EXEC 的兩半：
// 子程式**真的跑**（印出字、離開碼 42），而且**父程式拿得回控制權**
// （暫存器還原、CF 清 0、AH=4Dh 回 AX=0x002A）。
//
// 只做一半的典型症狀：子程式跑了但父程式永遠停機（看起來像 EXEC 成功），
// 或父程式回來了但 AH=4Dh 的 AH 留垃圾——GIN3.COM 拿整個 AX 比較，
// AH 不等於 0 會讓「回碼 0」被讀成非 0（spec §3）。
func TestExecRunsChildAndReturnsToParent(t *testing.T) {
	m, d := newTest(t)
	if err := os.WriteFile(filepath.Join(d.Root, "CHILD.EXE"), buildChildMZ(), 0o644); err != nil {
		t.Fatal(err)
	}

	// 父程式的執行環境：檔名字串與參數區放在 5000h 段。
	m.WriteBytes(0x50000, append([]byte("child.exe"), 0))
	m.Write16(0x50100, 0)      // env ＝ 繼承
	m.Write16(0x50102, 0x0120) // 尾巴 → 5000:0120
	m.Write16(0x50104, 0x5000)
	m.WriteBytes(0x50120, []byte{3, '9', ' ', '1', 0x0D})
	m.Write16(0x50106, 0x0140) // FCB1 → 5000:0140
	m.Write16(0x50108, 0x5000)
	m.Write16(0x5010A, 0x0150) // FCB2 → 5000:0150
	m.Write16(0x5010C, 0x5000)

	c := m.CPU
	c.Seg[cpu.DS] = 0x5000
	c.R[cpu.DX] = 0
	c.Seg[cpu.ES] = 0x5000
	c.R[cpu.BX] = 0x0100
	c.R[cpu.CX] = 0xBEEF // 父程式暫存器，要原樣回來
	call(m, d, 0x21, 0x4B00)

	if len(d.execStack) != 1 {
		t.Fatalf("EXEC 之後 execStack 深度要是 1，得到 %d", len(d.execStack))
	}
	if d.curPSP == machine.PSPSeg {
		t.Fatal("curPSP 沒切到子程式")
	}
	// 子程式 PSP+80h 要有尾巴。
	if got := m.Read8(uint32(d.curPSP)*16 + 0x80); got != 3 {
		t.Errorf("子程式 PSP+80h 的尾巴長度 ＝ %d，預期 3", got)
	}

	// 跑子程式直到它 exit（控制權回父程式，不停機）。
	for i := 0; i < 100 && !d.Exited && len(d.execStack) > 0; i++ {
		if err := m.Step(); err != nil {
			t.Fatal(err)
		}
	}
	if len(d.execStack) != 0 {
		t.Fatal("子程式沒有結束")
	}
	if d.Exited {
		t.Fatal("子程式 exit 把整台機器停掉了——父程式拿不回控制權")
	}
	if got := string(d.Console); got != "CHILD" {
		t.Errorf("主控台 ＝ %q，預期 \"CHILD\"", got)
	}
	if c.Flags&cpu.CF != 0 {
		t.Error("EXEC 成功回來時 CF 要是 0")
	}
	if c.R[cpu.CX] != 0xBEEF {
		t.Errorf("父程式 CX ＝ %04X，預期 BEEF（暫存器沒還原）", c.R[cpu.CX])
	}

	call(m, d, 0x21, 0x4D00)
	if got := c.R[cpu.AX]; got != 0x002A {
		t.Errorf("AH=4Dh 回 AX ＝ %04X，預期 002A（AH 必須是 0）", got)
	}
}

// TestExecMissingFileFailsAndParentSurvives：找不到檔 → CF=1、AX=2，
// 而且 execStack 不推東西（父程式狀態原封不動）。
func TestExecMissingFileFailsAndParentSurvives(t *testing.T) {
	m, d := newTest(t)
	m.WriteBytes(0x50000, append([]byte("nope.exe"), 0))
	c := m.CPU
	c.Seg[cpu.DS] = 0x5000
	c.R[cpu.DX] = 0
	c.Seg[cpu.ES] = 0x5000
	c.R[cpu.BX] = 0x0100
	call(m, d, 0x21, 0x4B00)
	if c.Flags&cpu.CF == 0 || c.R[cpu.AX] != 2 {
		t.Errorf("找不到檔要 CF=1、AX=2，得到 CF=%v AX=%04X",
			c.Flags&cpu.CF != 0, c.R[cpu.AX])
	}
	if len(d.execStack) != 0 || d.curPSP != machine.PSPSeg {
		t.Error("失敗的 EXEC 動到了 exec 狀態")
	}
}

// TestEMSQuerySubset 釘住 int 67h 的查詢子集（spec §4）。
func TestEMSQuerySubset(t *testing.T) {
	m, d := newTest(t)
	call(m, d, 0x67, 0x4000)
	if got := uint8(m.CPU.R[cpu.AX] >> 8); got != 0 {
		t.Errorf("AH=40h 要回 AH=0，得到 %02X", got)
	}
	call(m, d, 0x67, 0x4200)
	if m.CPU.R[cpu.BX] != 8 || m.CPU.R[cpu.DX] != 8 {
		t.Errorf("AH=42h 要回 BX=DX=8，得到 BX=%d DX=%d",
			m.CPU.R[cpu.BX], m.CPU.R[cpu.DX])
	}
	call(m, d, 0x67, 0x4400) // 映射：沒實作
	if got := uint8(m.CPU.R[cpu.AX] >> 8); got != 0x84 {
		t.Errorf("未實作的 EMS 功能要回 AH=84h，得到 %02X", got)
	}
}

// TestEMMDeviceOpensAndCloses：EMMXXXX0 開得到、關得掉、讀回 EOF（spec §5）。
func TestEMMDeviceOpensAndCloses(t *testing.T) {
	m, d := newTest(t)
	m.WriteBytes(0x50000, append([]byte("EMMXXXX0"), 0))
	m.CPU.Seg[cpu.DS] = 0x5000
	m.CPU.R[cpu.DX] = 0
	call(m, d, 0x21, 0x3D00)
	c := m.CPU
	if c.Flags&cpu.CF != 0 {
		t.Fatal("開 EMMXXXX0 失敗——launcher 會判定沒有 EMS 驅動")
	}
	h := c.R[cpu.AX]
	m.CPU.R[cpu.BX] = h
	m.CPU.R[cpu.CX] = 16
	call(m, d, 0x21, 0x3F00)
	if m.CPU.R[cpu.AX] != 0 {
		t.Errorf("裝置讀取要回 EOF（0），得到 %d", m.CPU.R[cpu.AX])
	}
	m.CPU.R[cpu.BX] = h
	call(m, d, 0x21, 0x3E00)
	if m.CPU.Flags&cpu.CF != 0 {
		t.Error("關 EMMXXXX0 失敗")
	}
}
