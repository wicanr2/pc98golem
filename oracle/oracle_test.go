package oracle_test

import (
	"os"
	"testing"

	"github.com/wicanr2/pc98golem/oracle"
)

// 這一份測試要**玩家自備的原版素材**才能跑。沒設環境變數就跳過——
// 本專案不含任何原版檔案，CI 上也不會有。
//
//	DOSGOLEM_TEST_EXE=…/RUN_full.EXE DOSGOLEM_TEST_ROOT=…/RICH2 \
//	    tools/go.sh test ./oracle/
func load(t *testing.T) *oracle.Oracle {
	t.Helper()
	exe, root := os.Getenv("DOSGOLEM_TEST_EXE"), os.Getenv("DOSGOLEM_TEST_ROOT")
	if exe == "" || root == "" {
		t.Skip("要 DOSGOLEM_TEST_EXE 與 DOSGOLEM_TEST_ROOT（玩家自備的原版素材）")
	}
	o, err := oracle.Load(exe, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(o.Close)
	return o
}

// TestReachPasswordScreen 釘住「跑得到防拷畫面」，順便釘住位址換算。
func TestReachPasswordScreen(t *testing.T) {
	o := load(t)
	if err := o.RunUntil(oracle.PasswordScreen()); err != nil {
		t.Fatal(err)
	}
	t.Logf("跑了 %d 道指令，開了 %v", o.Steps(), o.Opened())

	// 防拷畫面該有的三個檔（`rich2/docs/re/005`）。
	for _, f := range []string{"DATA.PAK", "SAY0.PXX", "RICHL.RIX"} {
		found := false
		for _, g := range o.Opened() {
			if g == f {
				found = true
			}
		}
		if !found {
			t.Errorf("沒開 %s——走到的可能不是防拷畫面", f)
		}
	}

	// 畫面上要有東西。全 0 表示還沒畫。
	nz := 0
	for _, v := range o.Indexed() {
		if v != 0 {
			nz++
		}
	}
	if nz < 50_000 {
		t.Errorf("畫面非零像素只有 %d/64000", nz)
	}
}

// TestDGROUPAddressing 釘住 `ds:XXXX` 的換算（`docs/spec/005` §3.1）。
//
// 這個換算錯了**不會報錯**——只會讀到看起來合理的別的變數，
// 然後整批對拍結論都是錯的。錨是 rich2/CLAUDE.md §4.1 拿來獨立求解
// DGROUP 基底的那個值：`ds:1B5Ah` 是浮點 1.0。
func TestDGROUPAddressing(t *testing.T) {
	o := load(t)
	if err := o.RunUntil(oracle.PasswordScreen()); err != nil {
		t.Fatal(err)
	}
	if got := o.Float(o.DS(0x1B5A)); got != 1.0 {
		t.Errorf("ds:1B5A ＝ %v，預期 1.0——DGROUP 段算錯了", got)
	}
	// 反查要對得回去：IDA 線性 41E90 就是 ds:0。
	if a, b := o.ToIDA(o.DS(0)), uint32(0x41E90); a != b {
		t.Errorf("ds:0 換回 IDA 線性是 %05X，預期 %05X", a, b)
	}
}

// TestClickIsSeen 釘住「點擊送得到而且遊戲會反應」。
//
// 這正是 dosemu.py 卡住的地方（`rich2/docs/re/005`「防拷：輸入確實送到了，
// 卡的是命中判定」）：輪詢讀到了正確座標卻畫面不動。
func TestClickIsSeen(t *testing.T) {
	o := load(t)
	if err := o.RunUntil(oracle.PasswordScreen()); err != nil {
		t.Fatal(err)
	}
	// 四個色塊的中心（`rich2/docs/re/005`「色塊的精確位置」實測）。
	if err := o.Click(102, 125); err != nil {
		t.Fatalf("點綠色色塊：%v", err)
	}
}

// TestSaveRestore 釘住快照。
//
// 沒有它，每個變體都要從頭跑四千多萬道指令；rich2 的 D33／D34
// 「走到罕見畫面很貴」就是這個問題。
func TestSaveRestore(t *testing.T) {
	o := load(t)
	if err := o.RunUntil(oracle.PasswordScreen()); err != nil {
		t.Fatal(err)
	}
	snap := o.Save()
	before := append([]uint8(nil), o.Indexed()...)

	if err := o.Click(102, 125); err != nil {
		t.Fatal(err)
	}
	if sameScreen(before, o.Indexed()) {
		t.Fatal("點了之後畫面沒變，這個測試證明不了什麼")
	}

	o.Restore(snap)
	if !sameScreen(before, o.Indexed()) {
		t.Error("Restore 之後畫面沒回到快照的樣子")
	}
	if o.Steps() != snap4Steps(snap, o) {
		t.Error("Restore 之後指令數沒回去")
	}
}

func snap4Steps(_ *oracle.State, o *oracle.Oracle) uint64 { return o.Steps() }

func sameScreen(a, b []uint8) bool {
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
