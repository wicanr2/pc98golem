// Package mscdrv 把 PC-98 的 MSCDRV 音樂驅動**跑起來**，攔它交給音源 BIOS
// 的演奏資料。
//
// 為什麼不靜態解析就好：靜態解析要把驅動的展開器重寫一遍，寫錯會安靜地錯。
// 跑原版自己的碼得到的是**另一個獨立來源**，兩邊逐位元組對得上才算數
// （spec 006 §4）。
//
// **一個位址都不寫死**：驅動自己把處理常式裝到 `IVT[7Eh]`，自己把回呼指標
// 填進工作區，本套件只照著走。
package mscdrv

import (
	"errors"
	"fmt"

	"github.com/wicanr2/pc98golem/internal/cpu"
	"github.com/wicanr2/pc98golem/internal/dos"
	"github.com/wicanr2/pc98golem/internal/machine"
	"github.com/wicanr2/pc98golem/internal/soundbios"
)

// PlayVector 是驅動安裝的中斷向量。**這不是設定，是觀察**——
// 安裝完之後由 [Driver.Vector] 回報實際裝在哪。
const PlayVector = 0x7E

// 工作區裡的版面。這些偏移是驅動自己填的，不是我們指定的；
// 本套件只在跑完安裝之後照著讀。
const (
	channelControlStride = 0x20 // 每聲道控制區
	callbackOffset       = 0x0C // 控制區 +0Ch/+0Eh ＝ 補資料的 far 指標
	blockTableOffset     = 0x08F6
	blockTableStride     = 4 // 指標 + 長度
	streamTableOffset    = 0x090E
	streamTableStride    = 0x44
)

// installBudget 是跑安裝路徑的指令數上限。
const installBudget = 20_000_000

// CallBudget 是一次呼叫的指令數上限。跑不完要**說出卡在哪**——
// 只說「跑不完」的話，「驅動在等東西」與「我們少實作了什麼」分不出來。
var CallBudget = 2_000_000

// sentinelSegment 是「跑完了」的哨兵位址。選在 BIOS 區之上，不會有程式碼。
const sentinelSegment = 0xFFF0

// Driver 是一顆跑起來的驅動。
type Driver struct {
	M    *machine.Machine
	DOS  *dos.DOS
	BIOS *soundbios.BIOS

	// Vector 是驅動實際裝上去的中斷向量內容。
	VectorSegment, VectorOffset uint16
	// DataSegment 是驅動放曲子資料的段（由第一次回呼觀察到）。
	DataSegment uint16

	channels int
}

// Block 是一個交給音源 BIOS 的演奏資料區塊。
type Block struct {
	// Offset 是**區塊標頭**在資料段裡的位置（第一個位元組是長度）。
	// 驅動的工作區記的是資料起點，也就是標頭之後一格；這裡減回來，
	// 讓執行出來的位址與靜態解析用同一套座標，才對得起來。
	Offset uint16
	Bytes  []byte
}

// Load 載入驅動並跑完它自己的安裝路徑。
func Load(image []byte, channels int) (*Driver, error) {
	if channels < 1 || channels > 16 {
		return nil, fmt.Errorf("聲道數 %d 不合理", channels)
	}
	m := machine.New()
	if err := m.LoadEXE(image); err != nil {
		return nil, fmt.Errorf("載入驅動：%w", err)
	}
	// 計時器中斷會打斷安裝路徑，而安裝不需要它。
	m.IRQ0Every = 0

	d := dos.New(m, "")
	d.Install()
	bios := soundbios.Install(m)

	driver := &Driver{M: m, DOS: d, BIOS: bios, channels: channels}
	if err := driver.runInstall(); err != nil {
		return nil, err
	}
	driver.VectorOffset = m.Read16(PlayVector * 4)
	driver.VectorSegment = m.Read16(PlayVector*4 + 2)
	if driver.VectorSegment == 0 && driver.VectorOffset == 0 {
		return nil, fmt.Errorf("驅動跑完了卻沒有裝 INT %02Xh——安裝路徑沒有走完", PlayVector)
	}
	return driver, nil
}

// runInstall 從進入點跑到 TSR。
func (d *Driver) runInstall() error {
	for step := 0; step < installBudget; step++ {
		if d.BIOS.Resident {
			return nil
		}
		if err := d.M.Step(); err != nil {
			return fmt.Errorf("安裝路徑第 %d 道指令：%w", step, err)
		}
		if d.M.CPU.Halted {
			return nil
		}
	}
	return fmt.Errorf("跑了 %d 道指令還沒 TSR——安裝路徑沒有收斂", installBudget)
}

// Play 叫驅動播第 track 首（0 起算），然後把每個聲道的演奏資料抽出來。
//
// maxBlocks 是每個聲道的抽取上限；會循環的曲子抽不完，**碰到上限會在
// Result.Truncated 標出來**。
func (d *Driver) Play(track, maxBlocks int) (Result, error) {
	result := Result{Channels: make([][]Block, d.channels)}
	// **曲號是 0 起算的。** 減一那一步在遊戲那邊的包裝裡（GAME.EXE 的播曲
	// 常式先 `dec` 再呼叫），不在驅動裡。傳 1 起算的話最後一首會落到表外，
	// 讀到的「指標」其實是曲子資料——症狀是驅動在自己的展開器裡空轉。
	if err := d.callInterrupt(PlayVector, uint16(track)); err != nil {
		stuck := &BudgetError{}
		if !errors.As(err, &stuck) {
			return result, err
		}
		// 驅動在自己的展開器裡空轉：照實記下來，繼續抽已經建好的聲道。
		result.Truncated = true
		result.Stuck = append(result.Stuck, stuck.Error())
	}
	if !d.BIOS.Playing && !result.Truncated {
		return result, fmt.Errorf("叫完第 %d 首之後 BIOS 沒有收到 PLAY", track+1)
	}
	work := uint32(d.BIOS.WorkSegment) * 16
	for channel := 0; channel < d.channels; channel++ {
		blocks, truncated, err := d.pump(work, channel, maxBlocks)
		if err != nil {
			stuck := &BudgetError{}
			if !errors.As(err, &stuck) {
				return result, fmt.Errorf("第 %d 首聲道 %d：%w", track+1, channel, err)
			}
			result.Truncated = true
			result.Stuck = append(result.Stuck,
				fmt.Sprintf("聲道 %d：%s", channel, stuck.Error()))
		}
		result.Channels[channel] = blocks
		if truncated {
			result.Truncated = true
		}
	}
	return result, nil
}

// Result 是一首曲子抽出來的東西。
type Result struct {
	Channels [][]Block
	// Truncated 為真代表有聲道是碰到上限才停的，不是曲子自己結束。
	Truncated bool
	// Stuck 記下驅動在哪裡空轉。**要印出來。**
	Stuck []string
}

// pump 反覆呼叫驅動的補資料常式，一次收一個區塊。這就是真的音源 BIOS
// 在缺資料時做的事。
func (d *Driver) pump(work uint32, channel, maxBlocks int) ([]Block, bool, error) {
	control := work + uint32(channel*channelControlStride)
	offset := d.M.Read16(control + callbackOffset)
	segment := d.M.Read16(control + callbackOffset + 2)
	if segment == 0 && offset == 0 {
		return nil, false, fmt.Errorf("聲道 %d 沒有補資料的回呼指標", channel)
	}
	streamAt := work + streamTableOffset + uint32(channel*streamTableStride)
	blockAt := work + blockTableOffset + uint32(channel*blockTableStride)

	var blocks []Block
	// 安裝路徑已經替每個聲道抽掉第一個區塊（`$008D` 對六個聲道各叫一次
	// 補資料常式）。真的音源 BIOS 是從演奏資料結構拿到那一個，所以這裡
	// 要先把它讀出來，不然序列會少掉開頭那一塊。
	if pointer := d.M.Read16(blockAt); pointer != 0 {
		if block, ok := d.readBlock(pointer, d.M.Read16(blockAt+2)); ok {
			blocks = append(blocks, block)
		}
	}
	for len(blocks) < maxBlocks {
		if d.M.Read16(streamAt) == 0 {
			return blocks, false, nil // 這個聲道的串流走完了
		}
		beforePointer, beforeLength := d.M.Read16(blockAt), d.M.Read16(blockAt+2)
		if err := d.callFar(segment, offset, uint16(channel)); err != nil {
			return blocks, false, err
		}
		pointer, length := d.M.Read16(blockAt), d.M.Read16(blockAt+2)
		// 串流走到結尾時驅動只把串流指標歸零，**不會動區塊表**——
		// 只看長度會把上一個區塊再收一次。反過來，**迴圈裡同一個區塊會被
		// 一交再交**，所以「區塊表沒變」單獨也不能當結束。兩個一起看才對。
		if d.M.Read16(streamAt) == 0 && pointer == beforePointer && length == beforeLength {
			return blocks, false, nil
		}
		block, ok := d.readBlock(pointer, length)
		if !ok {
			return blocks, false, fmt.Errorf("區塊長度 %d 不合理", length)
		}
		blocks = append(blocks, block)
	}
	return blocks, true, nil
}

// readBlock 從資料段讀一個區塊。pointer 是驅動記的資料起點。
func (d *Driver) readBlock(pointer, length uint16) (Block, bool) {
	if length == 0 || length > 0xFF {
		return Block{}, false
	}
	if d.DataSegment == 0 {
		d.DataSegment = d.M.CPU.Seg[cpu.DS]
	}
	base := uint32(d.DataSegment) * 16
	body := make([]byte, length)
	for i := range body {
		body[i] = d.M.Read8(base + uint32(pointer) + uint32(i))
	}
	return Block{Offset: pointer - 1, Bytes: body}, true
}

// callInterrupt 模擬一次 INT：推旗標與哨兵返回位址，跳到向量，跑到 iret 回哨兵。
func (d *Driver) callInterrupt(vector uint8, ax uint16) error {
	c := d.M.CPU
	sp := c.R[cpu.SP]
	c.R[cpu.AX] = ax
	offset := d.M.Read16(uint32(vector) * 4)
	segment := d.M.Read16(uint32(vector)*4 + 2)
	d.push(c.Flags)
	d.push(sentinelSegment)
	d.push(0)
	c.Seg[cpu.CS], c.IP = segment, offset
	return d.runToSentinel(sp, fmt.Sprintf("INT %02Xh", vector))
}

// callFar 模擬一次 far call：推哨兵返回位址，跳過去，跑到 retf 回哨兵。
func (d *Driver) callFar(segment, offset, ax uint16) error {
	c := d.M.CPU
	sp := c.R[cpu.SP]
	c.R[cpu.AX] = ax
	d.push(sentinelSegment)
	d.push(0)
	c.Seg[cpu.CS], c.IP = segment, offset
	return d.runToSentinel(sp, fmt.Sprintf("far call %04X:%04X", segment, offset))
}

// BudgetError 代表驅動在自己的碼裡跑不完。**這不一定是錯**：曲子的串流可以
// 用旗標做條件跳，而旗標的初值由遊戲決定；旗標沒設對時原版自己也會空轉。
// 呼叫端要據實標成截斷，不要當成成功。
type BudgetError struct {
	What     string
	Steps    int
	Segment  uint16
	Offset   uint16
	Relative uint32
}

func (e *BudgetError) Error() string {
	return fmt.Sprintf("%s 跑了 %d 道指令還沒回來，卡在 %04X:%04X（相對映像 $%X）",
		e.What, e.Steps, e.Segment, e.Offset, e.Relative)
}

func (d *Driver) push(v uint16) {
	c := d.M.CPU
	c.R[cpu.SP] -= 2
	d.M.Write16(cpu.Addr(c.Seg[cpu.SS], c.R[cpu.SP]), v)
}

// runToSentinel 跑到哨兵返回位址。超過預算時把 CS:IP 與 SP 復原到呼叫前，
// 讓下一次呼叫還能用同一台機器，並回 [BudgetError]。
func (d *Driver) runToSentinel(sp uint16, what string) error {
	c := d.M.CPU
	for step := 0; step < CallBudget; step++ {
		if c.Seg[cpu.CS] == sentinelSegment {
			return nil
		}
		if err := d.M.Step(); err != nil {
			return fmt.Errorf("%s 第 %d 道指令：%w", what, step, err)
		}
	}
	stuck := &BudgetError{
		What: what, Steps: CallBudget,
		Segment: c.Seg[cpu.CS], Offset: c.IP,
		Relative: cpu.Addr(c.Seg[cpu.CS], c.IP) - d.M.ImageBase,
	}
	c.Seg[cpu.CS], c.IP, c.R[cpu.SP] = sentinelSegment, 0, sp
	return stuck
}
