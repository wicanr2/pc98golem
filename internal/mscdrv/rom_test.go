package mscdrv

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wicanr2/pc98golem/internal/machine"
	"github.com/wicanr2/pc98golem/internal/soundbios"
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

// 真韌體自己把音序跑完。
//
// 計時器 ISR（`CEE0:0984`，韌體在 `CEE0:00A1` 自己寫進 `0000:0050` ＝ INT 14h）
// 每一格會巡六個聲道、送出該送的暫存器寫入，資料用完就從 `CEE0:0A43` 的
// `lcall [si+0Ch]` 回呼驅動要下一塊。
//
// 那個回呼指標要靠 [Driver.watchSupplyRoutines] 補回去——這一顆 `sound.rom`
// 的 INITIALIZE 會把驅動剛寫進去的指標清掉，理由與證據寫在那支函式上。
func TestRealROMSequencesTheMusicItself(t *testing.T) {
	image := original(t)
	rom := soundROM(t)
	driver, err := LoadWithROM(image, rom, 6)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Play(0, 8); err != nil {
		t.Fatal(err)
	}
	if vector, ok := driver.ROMTimerVector(); !ok || vector != TimerVector {
		t.Fatalf("計時器 ISR 掛在 INT %02Xh（找到=%v），預期 INT %02Xh",
			vector, ok, TimerVector)
	}
	if driver.SupplyRestored != 6 {
		t.Errorf("補回 %d 個補給常式指標，六個聲道應該都要補", driver.SupplyRestored)
	}
	before := len(driver.M.PortLog)
	if err := driver.Advance(240); err != nil {
		t.Fatalf("推 240 格計時器中斷：%v", err)
	}
	writes := driver.M.PortLog[before:]
	ports := map[uint16]int{}
	for _, w := range writes {
		ports[w.Port]++
	}
	// $188／$18A 是 OPN 的位址與資料埠；$08／$0A 是從屬 PIC（ISR 自己發 EOI）。
	if ports[0x188] < 100 || ports[0x18A] < 100 {
		t.Errorf("240 格才寫這麼少次 OPN：%v", ports)
	}
	// 每一次暫存器寫入都是「先寫位址埠再寫資料埠」，所以位址埠不會比較少。
	if ports[0x188] < ports[0x18A] {
		t.Errorf("位址埠寫得比資料埠少：%v", ports)
	}
	t.Logf("240 格計時器中斷：埠寫入 %d 次 %v", len(writes), ports)
}


// registerWrites 把埠寫入還原成 `(暫存器, 值)`：$188 選暫存器、$18A 寫值。
func registerWrites(log []machine.PortWrite) [][2]byte {
	var reg byte
	var out [][2]byte
	for _, w := range log {
		switch w.Port {
		case 0x188:
			reg = w.Val
		case 0x18A:
			out = append(out, [2]byte{reg, w.Val})
		}
	}
	return out
}

// 音色換算對拍：**拿真韌體自己寫出來的暫存器當標準答案**。
//
// 這是 NEC 那組反向刻度（`KS<<6|(31−AR)`、`31−DR`、`31−SR`、
// `(15−SL)<<4|(15−RR)`、`DETUNE<<4|MUL`）唯一的第一手驗證。先前只能說
// 「照抄會有幾首幾乎沒聲音，反過來就正常」——那是症狀，不是證據。
//
// 不比 `$40`..`$4F`（TL）：韌體先把四個運算元的 TL 寫成 `7F`（靜音）載入音色，
// 再由音量命令（`$8A`）把載波的 TL 蓋掉，**那是兩件事**；`Patch.Program`
// 只負責音色自己帶的那一份。
func TestFirmwareAgreesWithOurTimbreConversion(t *testing.T) {
	image := original(t)
	rom := soundROM(t)
	driver, err := LoadWithROM(image, rom, 6)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Play(0, 8); err != nil {
		t.Fatal(err)
	}
	before := len(driver.M.PortLog)
	if err := driver.Advance(4); err != nil {
		t.Fatal(err)
	}
	firmware := map[byte]byte{}
	for _, pair := range registerWrites(driver.M.PortLog[before:]) {
		// 只留第一次：之後的寫入是音量、調變與下一個聲道的事。
		if _, seen := firmware[pair[0]]; !seen {
			firmware[pair[0]] = pair[1]
		}
	}

	// 第 1 首聲道 0 的第一塊是 `85 01 90 34 42 01 …`——音色在資料段 $3490。
	software, err := Load(image, 6)
	if err != nil {
		t.Fatal(err)
	}
	result, err := software.Play(0, 4)
	if err != nil {
		t.Fatal(err)
	}
	const patchAt = 0x3490
	if len(result.Data) < patchAt+soundbios.PatchBytes {
		t.Fatalf("資料段快照只有 %d 位元組", len(result.Data))
	}
	first := result.Channels[0][0].Bytes
	if len(first) < 6 || first[0] != 0x85 ||
		int(first[2])|int(first[3])<<8 != patchAt {
		t.Fatalf("第一塊不是預期的音色載入：% X", first)
	}
	patch, err := soundbios.DecodePatch(result.Data[patchAt : patchAt+soundbios.PatchBytes])
	if err != nil {
		t.Fatal(err)
	}

	compared := 0
	patch.Program(func(register, value byte) {
		if register >= 0x40 && register <= 0x4F {
			return // TL 由音量命令決定，見上面的說明
		}
		got, ok := firmware[register]
		if !ok {
			t.Errorf("韌體沒有寫暫存器 $%02X（我們算 %02X）", register, value)
			return
		}
		if got != value {
			t.Errorf("$%02X：韌體寫 %02X，我們算 %02X", register, got, value)
		}
		compared++
	}, 0)
	if compared < 20 {
		t.Errorf("只對到 %d 個暫存器，太少——換算沒有真的被驗到", compared)
	}
	t.Logf("音色暫存器逐格相同：%d／%d", compared, compared)
}

// 音高換算對拍：韌體寫出來的 F-Number 與 block 要等於「音高碼除以 12」
// 配上 ROM 自己那張表。
//
// 第 1 首聲道 0 的第一個音是 `$2B`（43）：43÷12 ＝ 3 餘 7，所以 block 3、
// F-Number 是表的第 7 格。
func TestFirmwareAgreesWithOurNoteMapping(t *testing.T) {
	image := original(t)
	rom := soundROM(t)
	driver, err := LoadWithROM(image, rom, 6)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Play(0, 8); err != nil {
		t.Fatal(err)
	}
	before := len(driver.M.PortLog)
	if err := driver.Advance(4); err != nil {
		t.Fatal(err)
	}
	var high, low byte
	var got bool
	for _, pair := range registerWrites(driver.M.PortLog[before:]) {
		switch pair[0] {
		case 0xA4: // block 與 F-Number 高位；一定先寫這個
			high = pair[1]
		case 0xA0:
			if high != 0 && !got {
				low, got = pair[1], true
			}
		}
	}
	if !got {
		t.Fatal("韌體沒有寫聲道 0 的 F-Number（$A4／$A0）")
	}
	const note = 0x2B
	block := int(high>>3) & 7
	number := int(high&7)<<8 | int(low)
	if want := note / 12; block != want {
		t.Errorf("block %d，音高碼 $%02X ÷ 12 是 %d", block, note, want)
	}
	// ROM 的 FM F-Number 表在 ROM 內偏移 $3192，一格兩位元組。
	const fnumberTable = 0x3192
	semitone := note % 12
	at := fnumberTable + semitone*2
	want := int(rom[at]) | int(rom[at+1])<<8
	if number != want {
		t.Errorf("F-Number %d，ROM 表第 %d 格是 %d", number, semitone, want)
	}
	t.Logf("音高碼 $%02X → block %d、F-Number %d（ROM 表第 %d 格）",
		note, block, number, semitone)
}
