package mscdrv

import (
	"os"
	"path/filepath"
	"testing"

)

// 真的音源 BIOS ROM 不進版控。缺檔就 skip。
func soundROM(t *testing.T) []byte {
	t.Helper()
	dir := os.Getenv("PC98_BIOS_DIR")
	if dir == "" {
		t.Skip("沒有 PC98_BIOS_DIR：音源 BIOS ROM 不進版控")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "sound.rom"))
	if err != nil {
		t.Skipf("讀不到 sound.rom：%v", err)
	}
	return raw
}

// 拿真的韌體跑一次，看它往 OPN 埠寫了什麼。
// **這是驗我們那份換算的唯一硬證據**：軟體替身寫的是我們算的，
// 真 ROM 寫的是原版算的，兩邊要對得上。
func TestRealROMDrivesTheOPNPorts(t *testing.T) {
	image := original(t)
	rom := soundROM(t)
	driver, err := LoadWithROM(image, rom, 6)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("INT %02Xh → %04X:%04X（真 ROM），工作區段 $%04X",
		PlayVector, driver.VectorSegment, driver.VectorOffset, driver.BIOS.WorkSegment)
	if driver.VectorSegment == 0 && driver.VectorOffset == 0 {
		t.Fatal("驅動沒有裝 INT 7Eh")
	}
	before := len(driver.M.PortLog)
	if _, err := driver.Play(0, 8); err != nil {
		t.Fatalf("播第 1 首：%v", err)
	}
	writes := driver.M.PortLog[before:]
	if len(writes) == 0 {
		t.Fatal("真 ROM 一個埠都沒寫——韌體沒有真的跑起來")
	}
	ports := map[uint16]int{}
	for _, w := range writes {
		ports[w.Port]++
	}
	t.Logf("播放期間的埠寫入 %d 次：%v", len(writes), ports)
	// PC-9801-26K 的 OPN 在 $188（位址）與 $18A（資料）。
	if ports[0x188] == 0 || ports[0x18A] == 0 {
		t.Errorf("沒有寫到 OPN 的 $188／$18A——這不是音源 BIOS 在跑")
	}
}

// 演奏資料的命令寬度，直接對音源 BIOS 的分派表。
//
// ROM 裡有一張 `(opcode − $80) × 4` 索引的表：每項是 `{處理常式, 運算元數}`。
// **這是寬度的第一手來源**——先前那組寬度是用「每個區塊都要剛好收尾」推的，
// 推對了，但推對不等於有證據。
func TestCommandWidthsMatchTheROMDispatchTable(t *testing.T) {
	rom := soundROM(t)
	// 分派表在 CEE0:0B04。ROM 對映在 CC000h，所以 ROM 內偏移是 $2E00 + $B04。
	const table = 0x2E00 + 0x0B04
	// 分派器自己用 `cmp al,$8F; jae` 排除 $8F，所以表只有 $80..$8E 十五筆。
	for opcode := 0x80; opcode <= 0x8E; opcode++ {
		at := table + (opcode-0x80)*4
		operands := int(rom[at+2]) | int(rom[at+3])<<8
		want := 1 + operands
		if got := commandWidth(byte(opcode)); got != want {
			t.Errorf("$%02X：我們算 %d 位元組，ROM 的表寫 %d（運算元 %d 個）",
				opcode, got, want, operands)
		}
	}
	// 表的第 0 筆管的是「$80 以下」，也就是音符與休止。
	operands := int(rom[table+2]) | int(rom[table+3])<<8
	if want := 1 + operands; commandWidth(0x2B) != want {
		t.Errorf("音符：我們算 %d 位元組，ROM 的表寫 %d", commandWidth(0x2B), want)
	}
}

// 音源 BIOS 會把自己的計時器 ISR 掛到某個向量上。找出來，才有辦法在
// 沒有真硬體中斷的情況下推動音序。
func TestFindTheROMInterruptVectors(t *testing.T) {
	image := original(t)
	rom := soundROM(t)
	driver, err := LoadWithROM(image, rom, 6)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Play(0, 8); err != nil {
		t.Fatal(err)
	}
	for vector := 0; vector < 256; vector++ {
		offset := driver.M.Read16(uint32(vector) * 4)
		segment := driver.M.Read16(uint32(vector)*4 + 2)
		if segment >= 0xCC00 && segment <= 0xCFFF {
			t.Logf("INT %02Xh → %04X:%04X（ROM 內 $%X）",
				vector, segment, offset, uint32(segment)*16+uint32(offset)-0xCC000)
		}
	}
	// OPN 的計時器暫存器：$24/$25 是 Timer A、$26 是 Timer B、$27 是控制。
	var reg byte
	timers := map[byte]byte{}
	for _, w := range driver.M.PortLog {
		switch w.Port {
		case 0x188:
			reg = w.Val
		case 0x18A:
			if reg >= 0x24 && reg <= 0x27 {
				timers[reg] = w.Val
			}
		}
	}
	t.Logf("計時器暫存器最後寫入的值：%v", timers)
}
