package pool

import (
	"os"
	"path/filepath"
	"testing"
)

// 靜態反組譯讀出來的「ECL 區塊 → 曲號」對照表（spec 005）。
//
// **這裡的曲號是呼叫端用的 1 起算編號**（overlay 也是這樣推參數的：
// 商店 `mov al, 0Eh` ＝ 第 14 首）。播曲常式會先 `dec` 再送給驅動，
// 所以在中斷那一層看到的 `AL` 會少 1。兩套編號都對，別混用。
var staticTable = map[int]byte{
	0: 2, 8: 2, 11: 2,
	2: 3, 15: 3, 18: 3, 20: 3, 29: 3,
	14: 4, 21: 4, 24: 4,
	19: 5,
	1: 6, 13: 6, 16: 6, 17: 6, 28: 6,
	22: 7, 23: 7,
	10: 8,
	3: 9, 4: 9, 5: 9, 6: 9, 9: 9,
	7: 10,
	25: 13, 26: 13, 27: 13,
}

func gameEXE(t *testing.T) []byte {
	t.Helper()
	dir := os.Getenv("MSCDRV_DIR")
	if dir == "" {
		t.Skip("沒有 MSCDRV_DIR：原版執行檔不進版控")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "GAME.EXE"))
	if err != nil {
		t.Skipf("讀不到 GAME.EXE：%v", err)
	}
	return raw
}

// 主判準：**跑原版的碼**得到的對照表，要與靜態反組譯讀出來的逐筆相同。
// 兩個來源獨立——一個是模擬 CPU 上的實際查表，一個是人讀機器碼。
func TestAreaMusicTableMatchesTheDisassembly(t *testing.T) {
	game, err := Load(gameEXE(t))
	if err != nil {
		t.Fatal(err)
	}
	areas := make([]int, 0, 29)
	for area := 0; area <= 29; area++ {
		if area == 12 {
			continue // 原版沒有編號 12 的 ECL 區塊
		}
		areas = append(areas, area)
	}
	// 模式 3 是 ECL 派工，不在會擋掉區域配樂的 1／5／7 裡面。
	results, err := game.SweepAreaMusic(areas, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != len(areas) {
		t.Fatalf("掃了 %d 個區塊，預期 %d", len(results), len(areas))
	}
	var mismatch int
	for _, result := range results {
		want := staticTable[result.Area] - 1 // 常式先 dec 再送給驅動
		if result.Song != want {
			mismatch++
			t.Errorf("區塊 %2d：驅動收到第 %d 首，靜態表換算後應該是 %d（中斷 %+v）",
				result.Area, result.Song, want, result.Cues)
		}
	}
	if mismatch == 0 {
		t.Logf("29 個 ECL 區塊逐筆相同")
	}
}

// 音樂被關掉時不該派曲，而且要把目前曲號設成 $FF。
func TestMusicDisabledStopsInsteadOfPlaying(t *testing.T) {
	game, err := Load(gameEXE(t))
	if err != nil {
		t.Fatal(err)
	}
	game.M.Write8(game.data(musicEnabledAddress), 1)
	game.M.Write8(game.data(gameModeAddress), 3)
	game.M.Write8(game.data(areaNumberAddress), 0)
	game.cues = nil
	if err := game.callFar(game.codeBase+AreaMusicSegment, AreaMusicOffset); err != nil {
		t.Fatal(err)
	}
	for _, cue := range game.cues {
		if cue.Function == 0 {
			t.Errorf("音樂關掉了還派曲：%+v", cue)
		}
	}
	if got := game.M.Read8(game.data(currentSongAddress)); got != 0xFF {
		t.Errorf("目前曲號是 $%02X，預期 $FF", got)
	}
}

// 模式 1、5、7 有自己的曲子，區域配樂不該插手。
func TestModesWithOwnMusicAreLeftAlone(t *testing.T) {
	for _, mode := range []byte{1, 5, 7} {
		game, err := Load(gameEXE(t))
		if err != nil {
			t.Fatal(err)
		}
		results, err := game.SweepAreaMusic([]int{0}, mode)
		if err != nil {
			t.Fatal(err)
		}
		if results[0].Song != 0 {
			t.Errorf("模式 %d 之下區域配樂還是派了第 %d 首", mode, results[0].Song)
		}
	}
}
