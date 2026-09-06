// Package machine 是一台跑得動 `RUN_full.EXE` 的 8086 機器：
// 1 MB 平坦記憶體、BIOS 資料區、中斷向量表、I/O 埠。
//
// 規格在 `docs/spec/003-machine-and-loader.md`（READY）。
//
// **它不認識 DOS 服務**——那是 `internal/dos` 的事。這一層只負責
// 「記憶體長什麼樣」與「程式怎麼被載進去」。
package machine

import "github.com/wicanr2/pc98golem/internal/cpu"

// 記憶體佈局。取值沿用 `rich2/tools/dosemu.py` 那一版——**它是實際把
// `RUN.EXE` 跑到資產全部載完的那一份**，不是重新挑的。
const (
	// MemSize 是整個真實模式位址空間。
	MemSize = 1 << 20

	// MCBSeg 是假的記憶體控制區塊鏈起點，LOLSeg 是假的 DOS「list of lists」。
	MCBSeg = 0x0060
	LOLSeg = 0x0070

	// StubSeg 放中斷向量的預設目標。**不是只有一個 IRET**——見 §2。
	StubSeg = 0x0080

	// EnvSeg 是環境區塊；`PSP+2Ch` 指向它。
	EnvSeg = 0x0090

	// PSPSeg 是程式的 PSP，LoadSeg 是映像本體（PSP 佔 16 段）。
	PSPSeg  = 0x0100
	LoadSeg = PSPSeg + 0x10

	// MemTop 是傳統記憶體上緣（640 KB）。
	MemTop = 0x9FFF

	// VideoSeg 是 mode 13h 的畫面。
	VideoSeg   = 0xA000
	VideoWidth = 320
	VideoHigh  = 200
)

// mouseStubOff 是 `int 33h` 的向量指向的位移。
//
// `[HARD]` **那裡放的是 `90 CF`（`nop; iret`），不是 `CF`。**
// 滑鼠偵測直接讀 `0000:00CC` 拿到段:位移，再讀**那個位址的第一個位元組**，
// 是 `CFh` 就判定「沒有驅動」（`rich2/docs/re/182` §2）。
// 所有向量都指到同一個 IRET 的話，遊戲從此不發 `int 33h`——
// **而且沒有任何錯誤訊息**。
const mouseStubOff = 0x10

// DefaultIRQ0Every 是計時器中斷的間隔，單位是**指令數**。
//
// 真機是 18.2 Hz（55 ms）。以 DOSBox 預設的 3,000 cycles/ms 換算大約
// 165,000 道指令，這裡取整。用指令數而不是時間，是為了讓對拍決定性——
// 同一組輸入永遠得到同一個畫面。
const DefaultIRQ0Every = 165_000

// PortWrite 是一次埠寫入。音訊 parity 只需要這份序列，不必合成聲音
// （`docs/spec/004` §6）。
// OPLWrite 是一次 OPL2 暫存器寫入。
//
// **暫存器串是「樂譜」，波形是「演奏」。** 對拍暫存器串驗的是前者——
// 那是決定性的、可以逐筆比；波形要逐樣本一致屬於既定停止線
// （`rich2/docs/spec/049`）。
type OPLWrite struct {
	Reg  uint8
	Val  uint8
	Step uint64
}

type PortWrite struct {
	Port uint16
	Val  uint8
	// Step 是發生在第幾道指令，用來對齊時序。
	Step uint64
}

// Machine 是一台機器。用 New 造。
type Machine struct {
	Mem []uint8
	CPU *cpu.CPU

	// OPL 是解碼過的 OPL2 暫存器寫入序列（見 Out8）。
	// **這是音樂 parity 的對拍對象**：兩邊的暫存器串一樣，
	// 合成器再怎麼不同，送給晶片的指令是一樣的。
	OPL []OPLWrite

	// 寫入監看（WatchWrites）。watchLo > watchHi 表示關閉。
	watchLo, watchHi uint32
	onWrite          func(addr uint32, old, new uint8)

	// OPN（YM2203）的位址閂與狀態。PC-98 分歧。
	opnReg    uint8
	opnStatus uint8
	opnRegs   map[uint8]uint8

	oplReg          uint8
	oplPresent      bool
	oplTimerRunning bool

	// Ports 是每個埠最後一次寫進去的值；PortLog 是完整序列。
	Ports   map[uint16]uint8
	PortLog []PortWrite

	// PortsIn 是每個埠被讀了幾次。**輪詢埠的次數會很大**，那是正常的。
	PortsIn map[uint16]uint64

	// IRQ0Every 是每幾道指令送一次計時器中斷。0 ＝ 不送。
	//
	// 預設 DefaultIRQ0Every。**這個值影響動畫跑多快，不影響最終停下來的
	// 畫面**——防拷畫面是靜態的，動畫播完就穩定。
	IRQ0Every uint64

	// Ticks 是送出去的計時器中斷次數。
	Ticks uint64

	// portTicks 是所有 `in` 的累計，當作輪詢埠的時鐘。
	portTicks uint64

	nextIRQ0    uint64
	irq0Pending bool

	// DAC 是 VGA 調色盤，256×3 個 6 位元色值（`docs/formats/001` 的格式）。
	DAC [256 * 3]uint8

	dacIndex uint8
	dacPhase uint8

	// Steps 是已經執行的指令數。**不是週期數**——時序要等 M2
	// （`docs/spec/004` §5）。
	Steps uint64

	// FreeSeg 是映像之後第一個可配置的段。`int 21h AH=4Ah` 的探測要用它
	// （`docs/spec/004` §1.2）。
	FreeSeg uint16

	// ImageBase／ImageLen 是載進去的映像位置與長度。
	ImageBase uint32
	ImageLen  int
}

// New 造一台機器：記憶體清空、BDA 建好、向量表填好。
//
// **還沒有程式**——要呼叫 LoadEXE。
func New() *Machine {
	m := &Machine{
		Mem:       make([]uint8, MemSize),
		Ports:     map[uint16]uint8{},
		opnRegs:   map[uint8]uint8{},
		PortsIn:   map[uint16]uint64{},
		IRQ0Every: DefaultIRQ0Every,
		// 空區間 ＝ 監看關閉（見 WatchWrites）。零值的 lo=hi=0 會誤中位址 0。
		watchLo: 1, watchHi: 0,
	}
	m.CPU = cpu.New(m)
	// **這台機器是拿來跑 1993 年的 DOS 軟體的，不是拿來過語料的。**
	// `RUN_full.EXE` 的主程式區有 3,345 個 80186 的 `PUSH imm`；用 8086 的
	// 別名解讀會錯位一個 byte，然後安靜地飛掉（`docs/spec/002` §1.1）。
	// 語料驗收走 `cpu.New()`，那邊維持 8086 預設。
	m.CPU.Model = cpu.Model80186
	m.initBDA()
	m.initVectors()
	return m
}

// ---- cpu.Bus ------------------------------------------------------------

func (m *Machine) Read8(a uint32) uint8 { return m.Mem[a&0xFFFFF] }

func (m *Machine) Write8(a uint32, v uint8) {
	a &= 0xFFFFF
	if m.watchLo <= a && a <= m.watchHi && m.Mem[a] != v {
		m.onWrite(a, m.Mem[a], v)
	}
	m.Mem[a] = v
}

// WatchWrites 監看一段線性位址的寫入。
//
// **這是「誰寫這個位址」唯一直接的答案。** 靜態 xref 只涵蓋直接參考——
// `mov ds:XXXXh, ax` 抓得到，`mov [si+456h], ax` 抓不到，而後者正是
// 那些「掃不到寫入端」的變數的寫法（`rich2/CLAUDE.md` §4.1 第 4 條）。
//
// 只在**值真的變了**的時候通知，所以重複寫同一個值不會洗版。
// 傳 nil 關掉監看。CPU 的每一次寫入都走 Write8，所以 16 位寫入會來兩次。
func (m *Machine) WatchWrites(lo, hi uint32, fn func(addr uint32, old, new uint8)) {
	if fn == nil {
		m.watchLo, m.watchHi, m.onWrite = 1, 0, nil // 空區間 ＝ 永遠不命中
		return
	}
	m.watchLo, m.watchHi, m.onWrite = lo, hi, fn
}

// In8 回 0xFF。**空的匯流排上讀到的就是 0xFF，不是 0**——
// 有些偵測用「讀回來不是 FF」判定裝置存在，回 0 會讓它們誤判。
// In8 回應 `in`。
//
// ⚠ **輪詢埠的值一定要會變，否則程式死在等待迴圈裡**——而那看起來像
// 「程式沒走到那一步」，不像模擬器缺東西。VGA 的垂直回掃等待長這樣：
//
//	mov dx,0x3DA
//	in al,dx ; test al,8 ; jnz -5   ; 等這一次回掃結束
//	in al,dx ; test al,8 ; jz  -5   ; 等下一次回掃開始 ← 定值就死在這
//
// 不管回定值 0 還是定值 0FFh，兩段一定有一段轉不出來。
// 出處是 `rich2/tools/dosemu.py` 的 `on_in`（那支跑到畫出防拷畫面）。
//
// **這是行為模型，不是時序模型**：拿 `in` 的累計次數當時鐘，
// 不是週期精確的（時序在 M2，`docs/spec/004` §5）。
// SetAdLib 決定偵測時要不要讓 OPL2 存在。
//
// ⚠ **預設是不存在**，因為那讓開機少跑一大段。打開之後開機會慢，
// 但音樂路徑才會真的執行、`OPL` 才會有東西。
func (m *Machine) SetAdLib(present bool) { m.oplPresent = present }

// SignalOPNTimer 送一次計時器溢位：把狀態的 bit 0／1 立起來。
// ISR 會照協定寫 $27 的 bit 4／5 把它清掉。
func (m *Machine) SignalOPNTimer() { m.opnStatus |= 0x03 }

// OPNRegisters 是 OPN 每個暫存器最後被寫進去的值。
func (m *Machine) OPNRegisters() map[uint8]uint8 {
	out := make(map[uint8]uint8, len(m.opnRegs))
	for k, v := range m.opnRegs {
		out[k] = v
	}
	return out
}

func (m *Machine) In8(port uint16) uint8 {
	m.PortsIn[port]++
	m.portTicks++
	switch {
	case port == 0x3DA:
		// bit3 ＝ 垂直回掃、bit0 ＝ 顯示中。**兩個都要會變**，
		// 這樣不管程式等的是哪一種邊緣都轉得出來。
		if (m.portTicks>>4)&1 != 0 {
			return 0x09
		}
		return 0x00
	case port >= 0x40 && port <= 0x42:
		return uint8(-int(m.portTicks)) // PIT 是遞減計數器
	// ---- PC-98 分歧（spec 001 §4）：YM2203／OPN --------------------------
	//
	// $188 是位址／狀態埠、$18A 是資料埠（第二組在 $18C／$18E）。
	// **狀態的 bit 7 是忙碌旗標**，音源 BIOS 每寫一個暫存器前後都會輪詢它：
	//
	//	in al, dx ; shl al, 1 ; jb 回頭
	//
	// 預設回 $FF 的話 bit 7 永遠是 1，韌體會在那個迴圈裡轉到天荒地老——
	// 症狀是「安裝路徑跑不完」，看起來像 CPU 有問題。這裡永遠回不忙碌。
	//
	// bit 0／1 是兩個計時器的溢位旗標。**本層不做時序**：旗標由
	// SignalOPNTimer 明確送進來，ISR 依協定寫 $27 的 bit 4／5 清掉。
	// 這樣「音序有沒有往前走」是呼叫端看得見的決定，不是猜出來的時間。
	case port == 0x188 || port == 0x18C:
		return m.opnStatus
	case port == 0x18A || port == 0x18E:
		return m.Ports[port]

	case port == 0x388:
		// OPL2 狀態埠。
		//
		// **預設回 0 ＝ 偵測不到 AdLib，整段音樂路徑會被跳過**——那讓
		// 開機快很多，所以是預設。要對拍音樂就得讓偵測過關（`AdLib(true)`）。
		//
		// 過關要的不是一個定值：原版走的是標準的 AdLib 偵測序列
		// （`rich2/docs/re/011` §4），它**先要求狀態是 0、啟動計時器之後
		// 再要求是 0xC0**。回定值 0xC0 會在第一次檢查就被判定失敗，
		// 回定值 0 則在第二次失敗——兩種定值都過不了，所以這裡照著
		// 暫存器 04h 的寫入切換。
		if !m.oplPresent {
			return 0x00
		}
		if m.oplTimerRunning {
			return 0xC0 // bit7 IRQ ＋ bit6 timer1 逾時
		}
		return 0x00
	case port == 0x61:
		return 0x00
	}
	return 0xFF
}

func (m *Machine) Out8(p uint16, v uint8) {
	m.Ports[p] = v
	m.PortLog = append(m.PortLog, PortWrite{Port: p, Val: v, Step: m.Steps})

	// OPN（YM2203）：$188 選暫存器、$18A 寫值。PC-98 分歧（spec 001 §4）。
	switch p {
	case 0x188, 0x18C:
		m.opnReg = v
	case 0x18A, 0x18E:
		m.opnRegs[m.opnReg] = v
		if m.opnReg == 0x27 {
			// bit 4／5 是「清掉計時器 A／B 的溢位旗標」。ISR 靠它收尾，
			// 少了這一步旗標會一直立著，音序會被重複推進。
			if v&0x10 != 0 {
				m.opnStatus &^= 0x01
			}
			if v&0x20 != 0 {
				m.opnStatus &^= 0x02
			}
		}
	}

	// OPL2：0x388 選暫存器、0x389 寫值。**兩個埠是一組**，
	// 單看其中一個看不出寫了什麼。
	switch p {
	case 0x388:
		m.oplReg = v
	case 0x389:
		if m.oplReg == 0x04 { // 計時器控制
			switch {
			case v&0x80 != 0: // bit7 ＝ 重置 IRQ 與狀態
				m.oplTimerRunning = false
			case v&0x01 != 0: // bit0 ＝ 啟動計時器 1
				m.oplTimerRunning = true
			}
		}
		m.OPL = append(m.OPL, OPLWrite{Reg: m.oplReg, Val: v, Step: m.Steps})
	}

	// VGA DAC。**沒有它就只有色號沒有顏色**，而色號陣列自己看起來完全正常
	// ——畫面比對會變成「圖形對了但顏色全錯」，卻查不出顏色是誰的責任。
	switch p {
	case 0x3C8: // 設寫入索引
		m.dacIndex, m.dacPhase = v, 0
	case 0x3C9: // 連寫三次 ＝ R、G、B（各 6 位元）
		m.DAC[int(m.dacIndex)*3+int(m.dacPhase)] = v & 0x3F
		m.dacPhase++
		if m.dacPhase == 3 {
			m.dacPhase = 0
			m.dacIndex++ // 索引自動前進，所以整份調色盤可以一次寫完
		}
	}
}

// Palette 把 DAC 的 6 位元色值轉成 8 位元 RGB。
func (m *Machine) Palette() [256][3]uint8 {
	var out [256][3]uint8
	for i := 0; i < 256; i++ {
		for ch := 0; ch < 3; ch++ {
			v := m.DAC[i*3+ch]
			// 6 → 8 位元用「高位補到低位」，不是乘 255/63 四捨五入。
			out[i][ch] = v<<2 | v>>4
		}
	}
	return out
}

// ---- 記憶體存取的便利函式 ------------------------------------------------

func (m *Machine) Read16(a uint32) uint16 {
	return uint16(m.Mem[a&0xFFFFF]) | uint16(m.Mem[(a+1)&0xFFFFF])<<8
}

func (m *Machine) Write16(a uint32, v uint16) {
	m.Mem[a&0xFFFFF] = uint8(v)
	m.Mem[(a+1)&0xFFFFF] = uint8(v >> 8)
}

func (m *Machine) WriteBytes(a uint32, b []byte) {
	for i, v := range b {
		m.Mem[(a+uint32(i))&0xFFFFF] = v
	}
}

// Indexed 回傳 mode 13h 畫面的 320×200 色號陣列。
//
// **回的是色號不是 RGB**——對拍在色號空間做（`docs/spec/005` §3）。
// 回傳的是複本，呼叫端改它不會動到機器。
func (m *Machine) Indexed() []uint8 {
	out := make([]uint8, VideoWidth*VideoHigh)
	copy(out, m.Mem[VideoSeg*16:VideoSeg*16+len(out)])
	return out
}

// Step 執行一道指令，必要時先送 IRQ0。
func (m *Machine) Step() error {
	m.tick()
	m.Steps++
	return m.CPU.Step()
}

// tick 是計時器中斷（IRQ0 ＝ `int 08h`）。
//
// ⚠ **沒有它，任何等計時器的迴圈都轉不出來。** `RUN_full.EXE` 的防拷畫面
// 就停在這一個形狀上（執行期 `3014:167F` ＝ 線性 `406BF`）：
//
//	cmp cx, cs:[1727h]
//	jg  −5              ; 等計數器追上 CX
//
// `cs:1727h` 由程式自己的 ISR 遞增。中斷不送 ＝ 值永遠不變 ＝ 死迴圈，
// **而且畫面看起來是對的**——文字動畫停在第一個字，像是「還沒畫完」。
//
// **這是指令數模型，不是時間模型。** 拿執行過的指令數當時鐘，
// 好處是對拍完全決定性（同樣的輸入永遠得到同樣的畫面）；
// 代價是動畫速度與真機不同。週期精確的時序在 M2（`docs/spec/004` §5）。
func (m *Machine) tick() {
	if m.IRQ0Every > 0 && m.Steps >= m.nextIRQ0 {
		m.nextIRQ0 = m.Steps + m.IRQ0Every
		// **先掛起來，不要直接送。** 初始化期間大量 `CLI`，
		// 當場丟掉的話那一段的 tick 全部消失。
		m.irq0Pending = true
	}
	if !m.irq0Pending || !m.CPU.Flag(cpu.IF) {
		return
	}
	m.irq0Pending = false
	m.Ticks++
	m.bumpBDATicks()

	// 程式自己裝了 `int 08h` 就跑它的。
	if m.Read16(0x08*4+2) != StubSeg {
		m.CPU.Interrupt(0x08)
		return
	}
	// 否則做 BIOS 預設的事：更新計數之後轉呼 `int 1Ch`（那是給應用程式的
	// 掛鉤點，只裝 `1Ch` 不裝 `08h` 的程式很多）。
	if m.Read16(0x1C*4+2) != StubSeg {
		m.CPU.Interrupt(0x1C)
	}
}

// bumpBDATicks 推進 `0040:006C` 的 32 位元計數，並在跨日時設 `0040:0070`。
// 有些程式直接讀它算時間，不裝任何 ISR。
func (m *Machine) bumpBDATicks() {
	const at = 0x0040*16 + 0x6C
	v := uint32(m.Read16(at)) | uint32(m.Read16(at+2))<<16
	v++
	if v >= 0x001800B0 { // 一天的 tick 數
		v = 0
		m.Write8(0x0040*16+0x70, m.Read8(0x0040*16+0x70)+1)
	}
	m.Write16(at, uint16(v))
	m.Write16(at+2, uint16(v>>16))
}

// ---- 中斷向量表 ----------------------------------------------------------

// initVectors 把 256 個向量全部指到 StubSeg 的 stub。
//
// 兩件事同時要滿足（`docs/spec/003` §2）：
//
//  1. **每一個向量都要是合法位址。** 取到 `0000:0000` 的程式跳過去會執行
//     到垃圾（`rich2/docs/re/005` §3.2）。
//  2. **`int 33h` 的目標第一個位元組不能是 `CFh`。** 見 mouseStubOff。
func (m *Machine) initVectors() {
	m.Mem[StubSeg*16] = 0xCF                // iret
	m.Mem[StubSeg*16+mouseStubOff] = 0x90   // nop
	m.Mem[StubSeg*16+mouseStubOff+1] = 0xCF // iret
	for v := 0; v < 256; v++ {
		m.Write16(uint32(v)*4, 0)
		m.Write16(uint32(v)*4+2, StubSeg)
	}
	m.Write16(0x33*4, mouseStubOff)
	m.Write16(0x33*4+2, StubSeg)
}
