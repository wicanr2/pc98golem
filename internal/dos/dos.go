// Package dos 是 DOS 與 BIOS 服務層：`int 21h`、`int 10h`、`int 33h`、`int 16h`。
//
// 規格在 `docs/spec/004-dos-bios-services.md`（§2／§3／§4 都是 READY）。
//
// 三條貫穿全部的原則（`docs/spec/004` §1），每一條都是用一整輪換來的：
//
//  1. **未實作的服務只宣告成功，不碰呼叫端沒要求改的暫存器。**
//     清 `AX` 會讓「設中斷向量」的迴圈把 `AH` 變成 0 ＝ 結束程式。
//  2. **有些服務的「失敗」才是正確答案。** `AH=4Ah` 是記憶體探測。
//  3. **沒建的東西不會抱怨，只會給 0，而 0 是合法值。**
package dos

import (
	"fmt"
	"sort"

	"github.com/wicanr2/pc98golem/internal/cpu"
	"github.com/wicanr2/pc98golem/internal/machine"
)

// Time 是回給 `int 21h AH=2Ch` 的時刻。
//
// **固定值。** 時間會餵進亂數種子，浮動的話同一組按鍵每次跑到不同畫面
// （`rich2/CLAUDE.md` §8：截圖驗收要帶固定 seed）。
//
// **預設全 0，這是刻意與原版那邊的固定種子版對齊的。**
// `rich2/tools/patch_seed.py` 把 `TIMER` 內部的 `mov ah,2Ch / int 21h`
// 換成 `xor cx,cx / xor dx,dx`；我們這邊不改 binary，讓 `AH=2Ch` 直接
// 回 CX=DX=0，效果相同。兩邊的 `RANDOMIZE TIMER` 因此拿到同一個種子，
// 逐點對拍才有意義（`docs/spec/001` MVP-B）。
type Time struct{ Hour, Min, Sec, Hundredth uint8 }

// Mouse 是滑鼠狀態。座標用**像素**存，回報時才乘上 XScale
// （mode 13h 的標準驅動水平回報 0–639，`rich2/docs/re/182` §3）。
type Mouse struct {
	X, Y    uint16
	Buttons uint16
	// Press／Release 是 AX=5／AX=6 的統計，讀走就歸零。
	Press, Release uint16
	// XScale 是水平的虛擬座標倍率。**2 是標準**。
	XScale uint16
	// Polls 記下每一次 AX=3 回報出去的東西。**這是分辨「輸入沒送到」與
	// 「送到了但答錯」的唯一辦法**——兩者的畫面表現一模一樣。
	Polls []Poll

	// Sets 記下每一次 AX=4（程式自己設游標位置）。
	// 程式設過之後我們注入的位置就被蓋掉了，而症狀是「滑鼠移不動」。
	Sets []Poll

	// Calls 是每個 int 33h 功能號被叫了幾次。
	// **診斷「點了沒反應」的第一步**：先確認遊戲到底在讀哪一支。
	Calls map[uint16]int
}

// Poll 是一次 `AX=3` 的回報內容。
type Poll struct {
	X, Y, Buttons uint16
	Step          uint64
}

// DOS 是服務層。用 Install 掛到機器上。
type DOS struct {
	M *machine.Machine

	// Root 是原版素材的目錄（玩家自備）。**本專案不含任何原版檔案。**
	Root string

	// Now 是固定時刻，Mouse 是滑鼠狀態。
	Now   Time
	Mouse Mouse

	// Console 收 `AH=02h`／`06h`／`09h` 與 `int 10h AH=0Eh` 印出來的字。
	// **錯誤訊息走這條**，收不到就等於什麼都不知道。
	Console []byte

	// Calls 是每一種 (中斷, AH) 的呼叫次數；nil 表示不記。
	Calls map[Call]int

	// Stdin 是 `AH=3Fh` 讀 handle 0 時要餵的位元組；空的時候餵 StdinFill。
	//
	// ⚠ **不能回「讀到 0 個」**（等同 EOF）：主程式會當成輸入結束、
	// 還原中斷向量然後 exit（`rich2/docs/re/005`「死點修掉了」）。
	Stdin     []byte
	StdinFill uint8

	// StdinEmptyReadsZero 讓「佇列空」回報**讀到 0 個位元組**，
	// 而不是餵一個 StdinFill。
	//
	// **為什麼需要它**：編譯後 BASIC 的 `INKEY$` 空轉是
	// `while INKEY$ = "" : wend`（`rich2/docs/re/153` 的 `0x2CB42`：
	// `while ds:1094h == ds:2232h`，後者是空字串）。餵一個 `00` 會讓
	// `INKEY$` 拿到 `CHR$(0)`——**非空字串**，於是空轉立刻結束。
	// 症狀是**所有「按任意鍵繼續」的畫面一閃而過**：畫出來了、
	// 也真的畫對了，但幾十萬道指令之後就被下一次重繪蓋掉，
	// 而外面用粗取樣根本看不到它存在過。
	//
	// ⚠ **預設 false，不要全域打開。** 主程式在別的地方會把「讀到 0 個」
	// 當成 EOF，然後還原中斷向量並結束（那正是 StdinFill 存在的理由）。
	// 這個開關是給「我現在就是要讓某張畫面停住」用的，
	// 用完要關掉。
	StdinEmptyReadsZero bool

	// Drive 是 `AH=19h` 的目前磁碟（0 ＝ A:、1 ＝ B:、**2 ＝ C:**），
	// Dir 是 `AH=47h` 的目前目錄。
	//
	// ⚠ **預設一定要是 C:（硬碟）。** 回 A: 的話程式判定自己是從磁片跑，
	// 停在「Please put Disk#2 in A: and put Disk#3 in B:」等按鍵——
	// 而在接上 `AH=40h` 之前，那個畫面在主控台上是**一片空白**。
	Drive uint8
	Dir   string

	// Unimplemented 記下每一個沒實作的功能號被叫了幾次。
	//
	// **「宣告成功」本身也會說謊**：該填的緩衝區沒填就是垃圾，症狀出現在
	// 很後面而且完全不指向這裡。所以要數、要印（`docs/spec/004` §1.3）。
	Unimplemented map[Call]int

	// Opened 是開過的檔（依序），Missing 是找不到的。兩份都是診斷用。
	Opened  []string
	Missing []string

	// Wrote 記下「程式想寫檔」的每一次。**我們不寫**（原版素材唯讀），
	// 但安靜地報成功會讓「存檔壞掉」查不出來。
	Wrote []Write

	// Exited 為真表示程式呼叫了 `AH=4Ch`／`AH=00h`；ExitCode 是它的回傳碼。
	Exited   bool
	ExitCode uint8

	handles    map[uint16]*handle
	nextHandle uint16
	freeSeg    uint16

	// execStack 是 EXEC 的父程式堆疊（`docs/spec/007` §2）。
	// curPSP 是目前最內層程式的 PSP；lastExit／lastTerm 是最近一次
	// 子程式的回傳碼與結束方式（`AH=4Dh`）。
	execStack []execFrame
	curPSP    uint16
	lastExit  uint8
	lastTerm  uint8
}

// Write 是一次被擋下來的寫檔。
type Write struct {
	Name string
	N    int
}

// Call 是一次沒實作的服務呼叫：哪一個中斷、AH、AL。
type Call struct {
	Int, AH, AL uint8
}

func (c Call) String() string {
	return fmt.Sprintf("int %02Xh AH=%02X AL=%02X", c.Int, c.AH, c.AL)
}

// New 造一個服務層。root 是原版素材目錄。
func New(m *machine.Machine, root string) *DOS {
	return &DOS{
		M: m, Root: root,
		Now:           Time{}, // 全 0：與原版的固定種子版對齊，見 Time 的說明
		Mouse:         Mouse{XScale: 2, Calls: map[uint16]int{}},
		Drive:         2, // C:，見 Drive 欄位的說明
		Dir:           "RICH2",
		Unimplemented: map[Call]int{},
		handles:       map[uint16]*handle{},
		nextHandle:    5, // 0–4 是標準 handle
	}
}

// Install 把服務層掛到機器的中斷鉤子上。**要在 LoadEXE 之後叫**——
// 它會記下映像後面的第一個可配置段。
func (d *DOS) Install() {
	d.freeSeg = d.M.FreeSeg
	d.curPSP = machine.PSPSeg
	d.M.CPU.IntHook = d.handle
}

// handle 是中斷分派。回 true 表示「處理完了」，CPU 不走向量表。
//
// ⚠ **只在向量還指著我們的 stub 時才接手。** 程式會用 `AH=25h` 裝自己的
// 處理常式——最重要的是 `INT 34h`–`3Dh`（binary 自帶的 Microsoft 浮點
// 模擬器，全檔 876 個呼叫）。那些一定要讓 CPU 真的跳過去，
// 攔下來的話**所有浮點運算都會落空**，而 BASIC 的金錢運算全靠它。
//
// 這條規則也順便處理了計時器（`int 08h`／`1Ch`）與 Ctrl-Break（`int 23h`）：
// 程式裝了誰就跑誰的，不必逐個列白名單。
func (d *DOS) handle(c *cpu.CPU, n uint8) bool {
	if seg := d.M.Read16(uint32(n)*4 + 2); seg != machine.StubSeg {
		return false // 程式自己裝了處理常式
	}
	// Calls 記每一種 (中斷, AH) 呼叫過幾次。**只記不改行為**——
	// 「它到底在做什麼」在沒有畫面可看的時候只剩這個問得到。
	if d.Calls != nil {
		d.Calls[Call{Int: n, AH: uint8(c.R[cpu.AX] >> 8)}]++
	}
	switch n {
	case 0x21:
		d.int21(c)
	case 0x10:
		d.int10(c)
	case 0x33:
		d.int33(c)
	case 0x16:
		d.int16(c)
	case 0x11:
		// 取設備清單：就是 BDA 那一格（`docs/spec/003` §1）。
		// 不實作的話 AX 保持呼叫端傳進來的值，而顯示卡欄位是垃圾。
		c.R[cpu.AX] = d.M.Read16(0x0040*16 + 0x10)
	case 0x12:
		// 取常規記憶體大小，單位 KB。
		c.R[cpu.AX] = d.M.Read16(0x0040*16 + 0x13)
	case 0x13:
		d.int13(c)
	case 0x1A:
		d.int1A(c)
	case 0x20:
		d.exit(c, 0)
	case 0x67:
		d.int67(c)
	default:
		d.note(n, uint8(c.R[cpu.AX]>>8), uint8(c.R[cpu.AX]))
		clearCarry(c)
	}
	return true
}

func (d *DOS) exit(c *cpu.CPU, code uint8) {
	// EXEC 深度 > 0 時是子程式結束：回傳碼記下來，控制權還父程式
	// （`docs/spec/007` §2），不停機。
	if len(d.execStack) > 0 {
		d.childExit(c, code)
		return
	}
	d.Exited, d.ExitCode = true, code
	c.Halted = true
}

// note 記一筆沒實作的呼叫。
func (d *DOS) note(intNo, ah, al uint8) {
	d.Unimplemented[Call{Int: intNo, AH: ah, AL: al}]++
}

// UnimplementedReport 把統計排成可讀的清單，次數多的在前面。
//
// **收工前一定要看它。** 「跑得動」與「跑得動但行為不對」的差別就在這裡。
func (d *DOS) UnimplementedReport() []string {
	type row struct {
		c Call
		n int
	}
	rows := make([]row, 0, len(d.Unimplemented))
	for c, n := range d.Unimplemented {
		rows = append(rows, row{c, n})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].n != rows[j].n {
			return rows[i].n > rows[j].n
		}
		return rows[i].c.String() < rows[j].c.String()
	})
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, fmt.Sprintf("%s ×%d", r.c, r.n))
	}
	return out
}

// Close 關掉所有還開著的檔。
func (d *DOS) Close() {
	for _, h := range d.handles {
		if h.f != nil {
			h.f.Close()
		}
	}
	d.handles = map[uint16]*handle{}
}

// ---- 旗標與暫存器的半邊 --------------------------------------------------

func setCarry(c *cpu.CPU)   { c.SetFlags(c.Flags | cpu.CF) }
func clearCarry(c *cpu.CPU) { c.SetFlags(c.Flags &^ cpu.CF) }

// setAL／setAH 只動 AX 的一半，不碰另一半——原則 1。
func setAL(c *cpu.CPU, v uint8) { c.R[cpu.AX] = c.R[cpu.AX]&0xFF00 | uint16(v) }
func setAH(c *cpu.CPU, v uint8) { c.R[cpu.AX] = c.R[cpu.AX]&0x00FF | uint16(v)<<8 }

func setBL(c *cpu.CPU, v uint8) { c.R[cpu.BX] = c.R[cpu.BX]&0xFF00 | uint16(v) }
func setBH(c *cpu.CPU, v uint8) { c.R[cpu.BX] = c.R[cpu.BX]&0x00FF | uint16(v)<<8 }

func ah(c *cpu.CPU) uint8 { return uint8(c.R[cpu.AX] >> 8) }
func al(c *cpu.CPU) uint8 { return uint8(c.R[cpu.AX]) }
func bl(c *cpu.CPU) uint8 { return uint8(c.R[cpu.BX]) }
