// Package pool 是 PC-98 版《Pool of Radiance》專屬的觀測點。
//
// **位址寫在這裡，不寫進共用層**（spec 001 §4）：`internal/` 那幾層不該
// 認識任何一款遊戲。
//
// 這一支做的事是：把 `GAME.EXE` 的「區域配樂」程序**直接叫起來**，
// 對每一個 ECL 區塊記下它派哪一首曲子。用意是拿執行結果去對靜態反組譯
// 讀出來的那張表——兩個獨立來源對得上才算數。
//
// 不需要 GDC、不需要文字 VRAM、也不需要真的玩到那一格：那張表是純查表，
// 輸入只有一個全域變數。
package pool

import (
	"fmt"

	"github.com/wicanr2/pc98golem/internal/cpu"
	"github.com/wicanr2/pc98golem/internal/dos"
	"github.com/wicanr2/pc98golem/internal/machine"
)

// GAME.EXE 常駐段裡的位址，由反組譯讀出（Pool of Radiance，PC-98，
// Pony Canyon 1989-12-21）。都是**相對於載入映像**的。
const (
	// AreaMusicSegment／AreaMusicOffset 是「區域配樂」程序：
	// 讀目前區域 → 查表 → 派曲。呼叫端是 `GAME.EXE $5EB4` 與 overlay 26。
	AreaMusicSegment = 0x62C
	AreaMusicOffset  = 0x10C

	// 這三個是 DGROUP 裡的全域變數。
	areaNumberAddress   = 0x9D3F // 目前的 ECL 區塊
	currentSongAddress  = 0x9D40 // 目前播的曲號
	musicEnabledAddress = 0x9D42 // 1 代表音樂被關掉
	gameModeAddress     = 0x5ACE // 模式；1、5、7 會擋掉區域配樂
	delayCalibration    = 0x9D66 // Delay 的校準值；為 0 會除以零
)

// MusicVector 是音樂驅動掛的中斷向量。
const MusicVector = 0x7E

// sentinelSegment 是「跑完了」的哨兵位址。
const sentinelSegment = 0xFFF0

// hookSegment 是派曲攔截樁的位址。那裡放一個 `iret`。
//
// **不能靠 `IntHook`**：Turbo Pascal 的 `Intr` 不執行 `int` 指令——它從
// 向量表讀出目標，把旗標與返回位址推進堆疊，再 `retf` 跳過去。所以 CPU
// 看不到中斷，攔截要靠「執行到某個位址」。
const hookSegment = 0xFF00

// callBudget 是一次呼叫的指令數上限。
const callBudget = 20_000_000

// Cue 是一次派曲。
type Cue struct {
	// Function 是 `AH`：0 是播放、1 是停止。
	Function byte
	// Song 是 `AL`。**1 起算**——驅動那一邊會先減一。
	Song byte
}

// AreaCue 是一個 ECL 區塊查出來的結果。
type AreaCue struct {
	Area int
	// Cues 是這一次呼叫送出去的所有中斷，依序。
	Cues []Cue
	// Song 是最後一次播放命令的曲號；沒有派曲就是 0。
	Song byte
}

// Game 是一份載好的 GAME.EXE。
type Game struct {
	M   *machine.Machine
	DOS *dos.DOS

	dataSegment uint16
	codeBase    uint16
	cues        []Cue
}

// Load 載入 GAME.EXE。**不跑它的啟動碼**：我們只叫一支純查表的程序，
// 而啟動碼會去碰磁碟、GDC 與文字畫面。
//
// Turbo Pascal 的 DGROUP 與堆疊同段，所以 `DS` 取載入之後的 `SS`。
func Load(image []byte) (*Game, error) {
	m := machine.New()
	if err := m.LoadEXE(image); err != nil {
		return nil, fmt.Errorf("載入 GAME.EXE：%w", err)
	}
	m.IRQ0Every = 0
	d := dos.New(m, "")
	d.Install()

	game := &Game{
		M: m, DOS: d,
		dataSegment: m.CPU.Seg[cpu.SS],
		codeBase:    uint16(m.ImageBase / 16),
	}
	// 在向量表裝一個 iret 樁，跑到它就代表遊戲派了一次曲。
	m.Write16(MusicVector*4, 0)
	m.Write16(MusicVector*4+2, hookSegment)
	m.Write8(uint32(hookSegment)*16, 0xCF) // iret
	// 派曲之前原版會 `Delay(800)`。那個常式是空轉迴圈，圈數由這個校準值
	// 決定（原版在啟動時量出來）。我們不跑啟動碼，而且：
	//
	//   - 值是 0 會**除以零**；
	//   - 值太小會空轉五千萬道指令。
	//
	// 填一個讓它幾乎不等的值。**那 800 毫秒的靜音是原版的節奏，
	// 但對「派哪一首」沒有影響**——這一支量的是派曲，不是時序。
	m.Write16(game.data(delayCalibration), 0x8000)
	return game, nil
}

func (g *Game) data(offset uint16) uint32 { return cpu.Addr(g.dataSegment, offset) }

// SweepAreaMusic 對每一個 ECL 區塊叫一次區域配樂程序，記下它派哪一首。
//
// mode 是遊戲模式（`[$5ACE]`）：1、5、7 會讓程序直接返回，所以要傳一個
// 不在那三個裡面的值。
func (g *Game) SweepAreaMusic(areas []int, mode byte) ([]AreaCue, error) {
	out := make([]AreaCue, 0, len(areas))
	for _, area := range areas {
		g.M.Write8(g.data(musicEnabledAddress), 0) // 音樂開著
		g.M.Write8(g.data(gameModeAddress), mode)
		g.M.Write8(g.data(areaNumberAddress), byte(area))
		// 目前曲號設成 $FF：程序在「同一首」時什麼都不做，
		// 不重設的話第二個對到同一首的區塊會被判成沒派曲。
		g.M.Write8(g.data(currentSongAddress), 0xFF)

		g.cues = nil
		if err := g.callFar(g.codeBase+AreaMusicSegment, AreaMusicOffset); err != nil {
			return out, fmt.Errorf("區塊 %d：%w", area, err)
		}
		cue := AreaCue{Area: area, Cues: append([]Cue(nil), g.cues...)}
		for _, c := range g.cues {
			if c.Function == 0 {
				cue.Song = c.Song
			}
		}
		out = append(out, cue)
	}
	return out, nil
}

// callFar 模擬一次 far call，跑到 retf 回哨兵。
func (g *Game) callFar(segment, offset uint16) error {
	c := g.M.CPU
	c.Seg[cpu.DS] = g.dataSegment
	c.Seg[cpu.ES] = g.dataSegment
	c.R[cpu.SP] -= 2
	g.M.Write16(cpu.Addr(c.Seg[cpu.SS], c.R[cpu.SP]), sentinelSegment)
	c.R[cpu.SP] -= 2
	g.M.Write16(cpu.Addr(c.Seg[cpu.SS], c.R[cpu.SP]), 0)
	c.Seg[cpu.CS], c.IP = segment, offset

	for step := 0; step < callBudget; step++ {
		if c.Seg[cpu.CS] == sentinelSegment {
			return nil
		}
		if c.Seg[cpu.CS] == hookSegment && c.IP == 0 {
			g.cues = append(g.cues, Cue{
				Function: uint8(c.R[cpu.AX] >> 8),
				Song:     uint8(c.R[cpu.AX]),
			})
		}
		if err := g.M.Step(); err != nil {
			return fmt.Errorf("第 %d 道指令：%w", step, err)
		}
	}
	return fmt.Errorf("跑了 %d 道指令還沒回來，卡在 %04X:%04X", callBudget, c.Seg[cpu.CS], c.IP)
}
