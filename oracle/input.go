package oracle

import "fmt"

// 輸入（`docs/spec/005` §4）。
//
// **一切對齊指令數，不對齊牆上的時鐘。** rich2 現在 54 支腳本的瓶頸不是
// IPC 是 sleep——0.058 秒的 docker exec 旁邊掛著 0.35–2.2 秒的等待
// （`rich2/docs/spec/082` §1）。那些 sleep 存在的唯一理由是「主機沒辦法問
// 模擬器你跑完了沒」。在這裡可以問，所以不要再猜。

// MoveMouse 把游標移到某個像素座標。
//
// ⚠ **要在程式的 `AX=4` 之後叫**，否則會被它蓋掉而且畫面看起來完全正常。
// 用 Click 的話已經幫你等了。
func (o *Oracle) MoveMouse(x, y int) {
	o.d.Mouse.X, o.d.Mouse.Y = uint16(x), uint16(y)
}

// Mouse 回目前的游標座標。
func (o *Oracle) Mouse() (x, y int) {
	return int(o.d.Mouse.X), int(o.d.Mouse.Y)
}

// ClickOpt 調整一次點擊。
type ClickOpt func(*clickCfg)

type clickCfg struct {
	hover, hold, settle uint64
	watch               func(*Oracle)
}

// Hover 改「移到位置之後、按下之前」等多久。
func Hover(n uint64) ClickOpt { return func(c *clickCfg) { c.hover = n } }

// Watch 讓 Click 期間也取樣。
//
// ⚠ **沒有它的話，點擊那六百萬道指令是一段觀測不到的空窗。**
// `Click` 內部跑三段（hover／hold／settle），走 `o.Run`——條件函式一次都不會
// 被呼叫。實測棋子走的**第一格**常常就落在這段裡：`ds:1BE` 的軌跡因此
// 少一格，而序列其餘部分完全正確，看起來像「原版少走了一步」。
func Watch(f func(*Oracle)) ClickOpt { return func(c *clickCfg) { c.watch = f } }

// Hold 改按住的指令數。
func Hold(n uint64) ClickOpt { return func(c *clickCfg) { c.hold = n } }

// Settle 改放開之後再跑多久（讓遊戲把回饋畫出來）。
func Settle(n uint64) ClickOpt { return func(c *clickCfg) { c.settle = n } }

// Click 在某個像素座標點一下：移動 → 按下 → 按住 → 放開 → 等畫面回應。
//
// 三件事是實測出來的，每一件的反面都不會報錯：
//
//  1. **按住不能短。** 遊戲輪詢 `int 33h` 的頻率很低，按下與放開隔太近會
//     整個被跳過——DOSBox 那邊同一題要點三次才生效一次
//     （`rich2/docs/playtest/001` §5.6）。
//  2. **要等程式設過游標位置**（`AX=4`）才移動，見 MoveMouse。
//  3. **回 error**：點了畫面完全沒動要說出來，不要讓呼叫端拿「畫面沒變」
//     去猜是點錯位置還是遊戲還沒準備好。
func (o *Oracle) Click(x, y int, opts ...ClickOpt) error {
	cfg := clickCfg{hover: DefaultHover, hold: DefaultHold, settle: DefaultHold}
	for _, f := range opts {
		f(&cfg)
	}
	if len(o.d.Mouse.Sets) == 0 {
		if err := o.RunUntil(MouseSettled); err != nil {
			return fmt.Errorf("點 (%d,%d) 之前等程式設游標位置：%w", x, y, err)
		}
	}

	before := o.Indexed()
	o.MoveMouse(x, y)
	// ⚠ **移到位置之後要先停一下再按。**
	//
	// 頂端按鈕列是 hover-based：游標移過去先反白，反白之後的點擊才算數。
	// 移動與按下之間沒有間隔的話，遊戲在同一次輪詢裡同時看到新座標與
	// 按鍵——按鈕不會執行，只會反白。**畫面有反應**（真的反白了），
	// 所以看起來像「點到了但遊戲不理」。
	//
	// rich2 的 DOSBox 腳本用 `mousemove` → `sleep 0.4` → `mousedown`
	// 做同一件事（`tools/dosbox_session.py` 的 click）。
	if err := o.runWatched(cfg.hover, cfg.watch); err != nil {
		return fmt.Errorf("點 (%d,%d) 的 hover 期間：%w", x, y, err)
	}
	o.d.Mouse.Buttons = 1
	o.d.Mouse.Press++
	if err := o.runWatched(cfg.hold, cfg.watch); err != nil {
		return fmt.Errorf("點 (%d,%d) 按住期間：%w", x, y, err)
	}
	o.d.Mouse.Buttons = 0
	o.d.Mouse.Release++
	if err := o.runWatched(cfg.settle, cfg.watch); err != nil {
		return fmt.Errorf("點 (%d,%d) 放開之後：%w", x, y, err)
	}

	if sameBytes(before, o.Indexed()) {
		return &NoResponseError{X: x, Y: y, Polls: len(o.d.Mouse.Polls)}
	}
	return nil
}

// NoResponseError 是「點了但畫面一點都沒變」。
//
// 這在 dosemu.py（Python + unicorn 那一版）是個死結：輪詢讀到了正確座標
// 卻畫面不動，查了一整輪（`rich2/docs/re/005`「防拷：輸入確實送到了」）。
// 把它做成具名錯誤，是為了讓下一次遇到時**立刻知道是這個形狀**。
type NoResponseError struct {
	X, Y  int
	Polls int
}

func (e *NoResponseError) Error() string {
	return fmt.Sprintf("點 (%d,%d) 之後畫面完全沒變（期間滑鼠被輪詢 %d 次）"+
		"——不是座標不對，就是遊戲還沒準備好收這個點擊", e.X, e.Y, e.Polls)
}

// Type 把字串排進鍵盤佇列，餵給 `int 21h AH=3Fh`（BASIC 的 INKEY$）。
//
// ⚠ **遊戲的鍵盤輸入不走 `int 16h`**，走讀 handle 0
// （`rich2/docs/re/005`「輸入路徑」）。
func (o *Oracle) Type(s string) {
	o.d.Stdin = append(o.d.Stdin, []byte(s)...)
}

// Pending 回還沒被讀走的鍵數。
func (o *Oracle) Pending() int { return len(o.d.Stdin) }

// runWatched 跑 n 道指令，每一道都先呼叫 watch。watch 為 nil 時等同 Run。
func (o *Oracle) runWatched(n uint64, watch func(*Oracle)) error {
	if watch == nil {
		return o.Run(n)
	}
	start := o.m.Steps
	c := NewCond("跑滿指令數", func(o *Oracle) bool {
		watch(o)
		return o.m.Steps-start >= n
	})
	return o.RunUntil(c, Budget(n+1))
}

// ClearInput 丟掉還沒被讀走的按鍵。
//
// **上一張畫面沒吃掉的鍵會流進下一張。** 實測：對股市場所選單送了 ESC
// 而它沒有消化掉，那個 ESC 就留在佇列裡，於是下一張銀行選單一開就被
// 取消了——回傳 `0x63` 而不是我們要的第 1 列。
//
// 症狀是「明明送了 Enter，原版卻收到取消」，看起來像送鍵的方式錯了，
// 不像佇列有殘留。所以自動回答之前先清一次。
func (o *Oracle) ClearInput() { o.d.Stdin = nil }

// HoldOnEmptyInput 讓「鍵盤佇列空」回報**讀到 0 個位元組**。
//
// 編譯後 BASIC 的「按任意鍵繼續」是 `while INKEY$ = "" : wend`。
// dosgolem 預設在佇列空的時候餵一個 `00`（見 `dos.StdinFill` 的理由），
// 而 `CHR$(0)` 是**非空字串**——於是那種畫面**一閃而過**：
// 畫出來了、也畫對了，但下一次重繪就把它蓋掉，而外面用粗取樣
// 完全看不到它存在過（rich2 的地圖查詢就是這樣被誤判成「沒畫出來」）。
//
// ⚠⚠ **在 rich2 上它「停不住」畫面，只會把程式弄死。** 實測兩次：
// 打開之後主程式在別處的讀取拿到 0 個位元組 ＝ EOF，
// 直接 `int 21h AH=4Ch` 結束。
//
// **而且死掉之後畫面會凍在最後一幀**——那一幀正好是你想要的那張，
// 所以「非零像素穩定不變」看起來**很像成功停住了**。
// 判準要看 `Click`／`Run` 有沒有回錯，不是看畫面穩不穩。
//
// **想抓「畫完就等一個鍵」的畫面，正確做法是在它自然顯示的窗口內取樣**
// （rich2 的地圖查詢只顯示約四十萬道指令，而那段落在 `Click` 內部，
// 外面完全觀測不到）——用 `oracle.Watch` 加一個進入點的 `OnCall`。
//
// 這個開關留著是因為「讀到 0 個位元組」才是 DOS 的正確語意；
// 別的程式未必像 rich2 那樣把它當 EOF。用之前先確認。
func (o *Oracle) HoldOnEmptyInput(v bool) { o.d.StdinEmptyReadsZero = v }
