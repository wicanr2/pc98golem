package oracle

import (
	"fmt"

	"github.com/wicanr2/pc98golem/internal/cpu"
	"github.com/wicanr2/pc98golem/internal/machine"
)

// 執行與停止條件（`docs/spec/005` §3.2）。

// Cond 是停止條件。回 true 表示「到了」。
type Cond struct {
	name string
	// ready 每道指令**執行前**檢查一次。
	ready func(o *Oracle) bool
}

func (c Cond) String() string { return c.name }

// Ready 讓呼叫端把內建條件組合起來。
//
// ⚠ **有狀態的條件不能共用**（`ScreenIdle`／`PaletteIdle`／`WordIdle`
// 都記著「從哪一步開始沒變」）。組合時每個 Cond 各造一份，
// 不要把同一個實例用在兩個地方——那正是 `PasswordScreen` 從變數改成
// 函式的理由。
func (c Cond) Ready(o *Oracle) bool { return c.ready(o) }

// RunOpt 調整一次 RunUntil 的行為。
type RunOpt func(*runCfg)

type runCfg struct{ budget uint64 }

// Budget 改這一次的指令數上限。
func Budget(n uint64) RunOpt { return func(c *runCfg) { c.budget = n } }

// BudgetError 是「跑滿上限而條件沒成立」。
//
// ⚠ **這一定要是錯誤，不能靜靜地回來。** 條件寫錯與程式真的沒走到，
// 在「安靜地回來」之下長得一模一樣（`~/diagnosis-notes` 03）。
type BudgetError struct {
	Cond    string
	Budget  uint64
	Steps   uint64
	Stopped Addr
	Screen  int // 非零像素數，判斷「畫面上有沒有東西」
	Console string
}

func (e *BudgetError) Error() string {
	return fmt.Sprintf("跑滿 %d 道指令仍未達成「%s」：停在 %s，"+
		"畫面非零像素 %d，主控台 %q",
		e.Budget, e.Cond, e.Stopped, e.Screen, e.Console)
}

// ExitError 是「程式自己結束了」。
type ExitError struct {
	Code    uint8
	Console string
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("程式呼叫 int 21h AH=4Ch 離開，回傳碼 %d，主控台 %q",
		e.Code, e.Console)
}

// Run 純跑 n 道指令。程式中途結束或 CPU 出錯都回錯誤。
func (o *Oracle) Run(n uint64) error {
	return o.RunUntil(Steps(n), Budget(n+1))
}

// RunUntil 跑到條件成立。
//
// 條件在每道指令**執行前**檢查，所以 At(x) 停下來時 CS:IP 正好等於 x，
// 那一道還沒跑。
func (o *Oracle) RunUntil(c Cond, opts ...RunOpt) error {
	cfg := runCfg{budget: DefaultBudget}
	for _, f := range opts {
		f(&cfg)
	}
	deadline := o.m.Steps + cfg.budget

	for o.m.Steps < deadline {
		if c.ready(o) {
			return nil
		}
		if o.d.Exited {
			return &ExitError{Code: o.d.ExitCode, Console: o.Console()}
		}
		if o.m.CPU.Halted {
			return fmt.Errorf("CPU 停在 HLT（%s），條件「%s」未達成", o.IP(), c)
		}
		// 護欄：程式碼不該跑進 A0000 以上。那裡是視訊記憶體，在我們這台上
		// 全是 0，而 `00 00` ＝ `add [bx+si],al` 一路解得下去，
		// 所以飛掉之後**不會有任何錯誤**，只會安靜地跑滿上限。
		if a := o.IP().Linear(); a >= machine.VideoSeg*16 {
			return fmt.Errorf("跑出可用記憶體：%s（線性 %05X）", o.IP(), a)
		}
		o.fireCallHooks()
		if err := o.m.Step(); err != nil {
			return fmt.Errorf("執行到 %s 出錯：%w", o.IP(), err)
		}
	}
	nz := 0
	for _, v := range o.video() {
		if v != 0 {
			nz++
		}
	}
	return &BudgetError{
		Cond: c.String(), Budget: cfg.budget, Steps: o.m.Steps,
		Stopped: o.IP(), Screen: nz, Console: o.Console(),
	}
}

// ---- 內建條件 ------------------------------------------------------------

// Steps 是「再跑 n 道」。
func Steps(n uint64) Cond {
	var target uint64
	first := true
	return Cond{
		name: fmt.Sprintf("再跑 %d 道指令", n),
		ready: func(o *Oracle) bool {
			if first {
				target, first = o.m.Steps+n, false
			}
			return o.m.Steps >= target
		},
	}
}

// At 是「CS:IP 走到這裡」。位址通常用 o.IDA(...) 造。
func At(a Addr) Cond {
	return Cond{
		name:  "走到 " + a.String(),
		ready: func(o *Oracle) bool { return o.IP() == a },
	}
}

// Opened 是「程式開了這個檔」。
//
// **載入進度最可靠的路標**：比等指令數穩，比看畫面早。
// 名字不分大小寫，比 basename。
func Opened(name string) Cond {
	return Cond{
		name: "開了 " + name,
		ready: func(o *Oracle) bool {
			for _, f := range o.d.Opened {
				if equalFold(f, name) {
					return true
				}
			}
			return false
		},
	}
}

// ScreenIdle 是「畫面連續 n 道指令沒變」。
//
// ⚠ **這是「猜」的一種**，只是猜得比 sleep 準——畫面靜止不代表程式做完了
// （`rich2/docs/lessons.md` F48）。有明確路標時優先用 Opened 或 At。
//
// **取樣，不是每道指令都比。** 條件函式在每道指令上都會被呼叫，
// 而比一次畫面是 64 KB；乘上跑到防拷畫面的四千兩百萬道指令就完全動不了
// （第一版就是這樣，測試跑不完）。取樣間隔取 n/8，最多 20 萬道——
// 那仍然遠小於任何一段動畫，不會漏掉畫面在動。
func ScreenIdle(n uint64) Cond {
	every := n / 8
	if every > 200_000 {
		every = 200_000
	}
	if every < 1 {
		every = 1
	}
	var last []uint8
	var since, nextCheck uint64
	return Cond{
		name: fmt.Sprintf("畫面連續 %d 道指令沒變", n),
		ready: func(o *Oracle) bool {
			if o.m.Steps < nextCheck {
				return false
			}
			nextCheck = o.m.Steps + every
			cur := o.video()
			if last == nil {
				last = append([]uint8(nil), cur...)
				since = o.m.Steps
				return false
			}
			if !sameBytes(last, cur) {
				last = append(last[:0], cur...)
				since = o.m.Steps
				return false
			}
			return o.m.Steps-since >= n
		},
	}
}

// NewCond 造一個自訂條件。
//
// ready 在**每道指令執行前**被呼叫，所以它必須便宜——
// 每道指令都做一次 64 KB 的比對，跑四千萬道就完全動不了（實測過）。
// 需要昂貴的檢查就自己取樣，內建的 ScreenIdle 就是這樣寫的。
func NewCond(name string, ready func(*Oracle) bool) Cond {
	return Cond{name: name, ready: ready}
}

// PaletteIdle 是「調色盤連續 n 道指令沒變」。
//
// **等淡入用這個，不要等畫面靜止。** 淡入是調色盤動畫（`rich2/docs/re/146`），
// 而棋盤上的角色一直在動——畫面永遠不會靜止，`ScreenIdle` 在那裡跑不完。
//
// skip 回 true 的色號不列入比較。傳 nil 表示全部都比。
// **循環動畫的色號要 skip 掉**，否則調色盤永遠在變。
func PaletteIdle(n uint64, skip func(i int) bool) Cond {
	every := n / 8
	if every > 200_000 {
		every = 200_000
	}
	if every < 1 {
		every = 1
	}
	var last [256][3]uint8
	var have bool
	var since, nextCheck uint64
	return Cond{
		name: fmt.Sprintf("調色盤連續 %d 道指令沒變", n),
		ready: func(o *Oracle) bool {
			if o.m.Steps < nextCheck {
				return false
			}
			nextCheck = o.m.Steps + every
			cur := o.Palette()
			same := have
			for i := 0; i < 256 && same; i++ {
				if skip != nil && skip(i) {
					continue
				}
				if cur[i] != last[i] {
					same = false
				}
			}
			if !same {
				last, have, since = cur, true, o.m.Steps
				return false
			}
			return o.m.Steps-since >= n
		},
	}
}

// WordIdle 是「某個變數連續 n 道指令沒變」。
//
// 用來等「動作做完」：棋子在走的時候格號一直在跳，停下來才算走完。
// 比等畫面靜止可靠——棋盤上的角色動畫讓畫面永遠不會靜止。
func WordIdle(a Addr, n uint64) Cond {
	every := n / 8
	if every > 100_000 {
		every = 100_000
	}
	if every < 1 {
		every = 1
	}
	var last uint16
	var have bool
	var since, nextCheck uint64
	return Cond{
		name: fmt.Sprintf("%s 連續 %d 道指令沒變", a, n),
		ready: func(o *Oracle) bool {
			if o.m.Steps < nextCheck {
				return false
			}
			nextCheck = o.m.Steps + every
			cur := o.Word(a)
			if !have || cur != last {
				last, have, since = cur, true, o.m.Steps
				return false
			}
			return o.m.Steps-since >= n
		},
	}
}

// MousePolled 是「滑鼠又被輪詢了 n 次」。
//
// ⚠ **這是「遊戲在等輸入」最直接的訊號。**
//
// 「畫面畫完」不等於「準備好收輸入」：進棋盤之後遊戲還在跑收尾動畫，
// 那段期間**一次都不讀滑鼠**——點下去等於沒點，而畫面看起來完全正常
// （游標甚至會反白按鈕，因為反白是遊戲自己畫的）。
//
// 實測：`ToBoard` 回來之後直接點，Click 的六百萬道指令內輪詢 0 次。
func MousePolled(n int) Cond {
	var base int
	first := true
	return Cond{
		name: fmt.Sprintf("滑鼠再被輪詢 %d 次", n),
		ready: func(o *Oracle) bool {
			if first {
				base, first = len(o.d.Mouse.Polls), false
			}
			return len(o.d.Mouse.Polls)-base >= n
		},
	}
}

// MouseSettled 是「程式已經自己設過游標位置」。
//
// 程式進防拷畫面時用 `int 33h AX=4` 設一次游標位置（實測在第 42,406,064 道）。
// **在那之前移動滑鼠會被它蓋掉**，而且畫面看起來完全正常
// ——差異一個像素都不會少（`docs/spec/005` §4）。
var MouseSettled = Cond{
	name:  "程式設過游標位置",
	ready: func(o *Oracle) bool { return len(o.d.Mouse.Sets) > 0 },
}

// PasswordScreen 是「防拷密碼畫面已經畫好」。
//
// 判準是程式設過游標位置**而且**畫面靜下來——前者是明確的程式行為，
// 後者擋掉「設完游標但文字動畫還在跑」。
//
// ⚠ **這是函式不是變數，而且不能改回變數。**
//
// 它裡面的 ScreenIdle 帶狀態（上一張畫面、從哪一步開始沒變）。
// 寫成套件層級的 `var` 的話，**所有 Oracle 共用同一份計數器**——
// 第二次跑會沿用第一次留下的 `since`，於是同一條路徑走出不同的指令數。
// 實測症狀：連續三次走到棋盤是 230,316,290 / 230,481,291 / 230,646,291，
// 每次多一個 PIT 週期，而畫面看起來完全正常。
//
// 同一個形狀對任何「內建條件」都成立：**有狀態的條件必須是函式。**
func PasswordScreen() Cond {
	idle := ScreenIdle(3_000_000)
	return Cond{
		name: "防拷密碼畫面",
		ready: func(o *Oracle) bool {
			return MouseSettled.ready(o) && idle.ready(o)
		},
	}
}

// ---- call hook -----------------------------------------------------------

// OnCall 在 CS:IP 走到 addr 時呼叫 fn。
//
// 用途是**把判準從像素換成參數**：原版自己傳給繪製常式的座標與字串，
// 比從畫面反推可靠（`rich2/docs/lessons.md` D34 就是像素判準誤中）。
func (o *Oracle) OnCall(a Addr, fn func(*Oracle)) {
	o.onCall[a.Linear()] = append(o.onCall[a.Linear()], fn)
}

func (o *Oracle) fireCallHooks() {
	if len(o.onCall) == 0 {
		return
	}
	if hooks, ok := o.onCall[o.IP().Linear()]; ok {
		for _, fn := range hooks {
			fn(o)
		}
	}
}

// Caller 回 far call 的返回位址，也就是**呼叫端的下一道指令**。
//
// ⚠ **只在剛進入被呼叫的常式時有效**（`OnCall` 的 hook 裡）。
// 常式一旦推了東西上堆疊，`SP` 就不再指向返回位址。
//
// 用途是回答「誰在用這支常式」——例如 `RND` 有幾個呼叫端、
// 擲骰與抽籤各消耗幾次亂數。
func (o *Oracle) Caller() Addr {
	ss, sp := o.m.CPU.Seg[cpu.SS], o.m.CPU.R[cpu.SP]
	ip := o.m.Read16(cpu.Addr(ss, sp))
	cs := o.m.Read16(cpu.Addr(ss, sp+2))
	return Addr{cs, ip}
}

// Arg 讀 far call 的第 n 個參數（n 從 0 起，最後推的是第 0 個）。
//
// **參數個數看 `retf N`（N/2），不是進場的 `mov bx`**
// （`rich2/docs/re/086`）。進到常式時堆疊上是：回返位址 4 bytes，然後是參數。
func (o *Oracle) Arg(n int) uint16 {
	sp := o.m.CPU.R[cpu.SP] + 4 + uint16(n)*2
	return o.m.Read16(cpu.Addr(o.m.CPU.Seg[cpu.SS], sp))
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if 'a' <= x && x <= 'z' {
			x -= 32
		}
		if 'a' <= y && y <= 'z' {
			y -= 32
		}
		if x != y {
			return false
		}
	}
	return true
}

func sameBytes(a, b []uint8) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ── 動畫相位 ───────────────────────────────────────────────────────

// Ticks 回目前送出去的計時器中斷次數。
//
// **這是動畫的相位。** 原版的動畫每個計時器刻前進一格（擲骰動畫就是
// 每刻擲一次兩顆，`rich2/docs/re/185` §2b），所以「同一個 Ticks」
// 就是「同一個動畫相位」。
//
// 它由**指令數**驅動（`IRQ0Every`），不是真實時間，所以是決定性的：
// 同一個輸入跑兩次會停在同一刻。
func (o *Oracle) Ticks() uint64 { return o.m.Ticks }

// AtTick 是「跑到第 n 個計時器刻」。
//
// 畫面對拍要的是**同狀態同相位**：狀態靠存檔與操作序列決定，
// 相位靠這個。以前那條路只能拿 DOSBox 截圖去猜相位
// （`rich2/docs/playtest/054` §3.4），現在可以指定。
func AtTick(n uint64) Cond {
	return Cond{
		name:  fmt.Sprintf("第 %d 個計時器刻", n),
		ready: func(o *Oracle) bool { return o.m.Ticks >= n },
	}
}

// TickRate 讀寫「每幾道指令送一次計時器中斷」。
//
// ⚠ **改它會改變動畫跑多快，但不改變最終停在哪裡**（`machine` 的註解）。
// 調小可以讓動畫在較少的指令內走完，代價是與原版在真機上的節奏不同——
// 所以**畫面相位對拍時不要動它**，只在趕時間的探索性測試裡用。
func (o *Oracle) TickRate() uint64         { return o.m.IRQ0Every }
func (o *Oracle) SetTickRate(every uint64) { o.m.IRQ0Every = every }

// StackWord 讀堆疊上的第 i 個 word（i ＝ 0 是 `SS:SP` 指的那一個）。
//
// ⚠ **只在剛進入被呼叫的常式時有意義**——常式一旦 `push` 了東西，
// `SP` 就不再指向返回位址。與 `Caller` 同一個限制。
func (o *Oracle) StackWord(i int) uint16 {
	ss, sp := o.m.CPU.Seg[cpu.SS], o.m.CPU.R[cpu.SP]
	return o.m.Read16(cpu.Addr(ss, sp+uint16(2*i)))
}

// AX 讀回傳值所在的暫存器。BASIC 的函式用它回傳整數。
func (o *Oracle) AX() uint16 { return o.m.CPU.R[cpu.AX] }
