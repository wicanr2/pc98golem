package oracle

import (
	"github.com/wicanr2/pc98golem/internal/dos"
	"github.com/wicanr2/pc98golem/internal/machine"
)

// State 是一份機器狀態的快照（`docs/spec/005` §5）。
//
// 用途是**從同一個狀態展開多個變體**：開機到防拷畫面要四千兩百萬道指令，
// 走到棋盤還要更多。rich2 的 D33／D34「走到罕見畫面很貴，最後改存檔資料」
// 就是這個問題。
//
// ⚠ **不落地成檔案。** 這裡面是原版的整份記憶體映像，寫到磁碟等於散布它。
type State struct {
	// machine 那一半（記憶體、CPU、**所有內部時鐘**）由 machine 自己拍。
	// 時鐘漏一個就會讓還原之後的計時器中斷不再送，見 machine.Snapshot 的說明。
	m *machine.Snapshot

	mouse dos.Mouse
	stdin []byte
	// 開過的檔要一起存，否則 Restore 之後 Opened() 的路標會回到過去。
	opened []string
}

// mem 讓同一個套件裡的搜尋拿得到快照的記憶體。
func (s *State) mem() []uint8 { return s.m.Mem() }

// Save 深拷貝目前的狀態。1 MB 記憶體，大約 1 毫秒。
func (o *Oracle) Save() *State {
	s := &State{
		m:      o.m.Snapshot(),
		mouse:  o.d.Mouse,
		stdin:  append([]byte(nil), o.d.Stdin...),
		opened: append([]string(nil), o.d.Opened...),
	}
	// Polls 是切片，共用底層陣列會讓快照跟著之後的執行變動。
	s.mouse.Polls = append([]dos.Poll(nil), o.d.Mouse.Polls...)
	s.mouse.Sets = append([]dos.Poll(nil), o.d.Mouse.Sets...)
	return s
}

// Restore 把機器倒回某個快照。
//
// ⚠ **開著的檔不會倒回去。** 檔案位置是作業系統的狀態，不在這份快照裡；
// 快照之後才開的檔會留著開著。實務上沒問題（程式開檔就讀完），
// 但要知道這個邊界。
func (o *Oracle) Restore(s *State) {
	o.m.Restore(s.m)
	o.d.Mouse = s.mouse
	o.d.Mouse.Polls = append([]dos.Poll(nil), s.mouse.Polls...)
	o.d.Mouse.Sets = append([]dos.Poll(nil), s.mouse.Sets...)
	o.d.Stdin = append([]byte(nil), s.stdin...)
	o.d.Opened = append([]string(nil), s.opened...)
	o.d.Exited = false
}
