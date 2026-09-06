package mscdrv

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// 原版驅動不進版控。缺檔就 skip——不做代用品。
func original(t *testing.T) []byte {
	t.Helper()
	dir := os.Getenv("MSCDRV_DIR")
	if dir == "" {
		t.Skip("沒有 MSCDRV_DIR：原版驅動不進版控")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "MSCDRV.EXE"))
	if err != nil {
		t.Skipf("讀不到 MSCDRV.EXE：%v", err)
	}
	return raw
}

// 驅動要能自己跑完安裝路徑，並且把處理常式裝到向量表上。
func TestDriverInstallsItself(t *testing.T) {
	driver, err := Load(original(t), 6)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("INT %02Xh → %04X:%04X，工作區段 $%04X，音源 BIOS 呼叫 %d 次",
		PlayVector, driver.VectorSegment, driver.VectorOffset,
		driver.BIOS.WorkSegment, len(driver.BIOS.Calls))
	if len(driver.BIOS.Unhandled) > 0 {
		t.Logf("沒實作的音源 BIOS 命令：%v", driver.BIOS.Unhandled)
	}
}

// 每一首都要抽得到演奏資料。
func TestEveryTrackYieldsBlocks(t *testing.T) {
	image := original(t)
	for track := 0; track < 15; track++ {
		driver, err := Load(image, 6)
		if err != nil {
			t.Fatal(err)
		}
		result, err := driver.Play(track, 64)
		if err != nil {
			t.Errorf("第 %d 首：%v", track+1, err)
			continue
		}
		total, empty := 0, 0
		for _, blocks := range result.Channels {
			total += len(blocks)
			if len(blocks) == 0 {
				empty++
			}
		}
		if total == 0 {
			t.Errorf("第 %d 首一個區塊都沒抽到", track+1)
		}
		t.Logf("第 %2d 首：%3d 個區塊，空聲道 %d，截斷 %v",
			track+1, total, empty, result.Truncated)
	}
}

func mzImage(raw []byte) []byte {
	header := int(binary.LittleEndian.Uint16(raw[8:10])) * 16
	return raw[header:]
}

// 診斷：第 15 首抽不到東西，先看驅動把串流指標填成什麼。
func TestTrackFifteenStreamPointers(t *testing.T) {
	image := original(t)
	for _, track := range []int{0, 14} {
		driver, err := Load(image, 6)
		if err != nil {
			t.Fatal(err)
		}
		result, err := driver.Play(track, 4)
		if err != nil {
			t.Fatalf("第 %d 首：%v", track+1, err)
		}
		work := uint32(driver.BIOS.WorkSegment) * 16
		var pointers, callbacks []string
		for channel := 0; channel < 6; channel++ {
			pointers = append(pointers, fmtHex(driver.M.Read16(
				work+streamTableOffset+uint32(channel*streamTableStride))))
			control := work + uint32(channel*channelControlStride)
			callbacks = append(callbacks, fmtHex(driver.M.Read16(control+callbackOffset+2))+
				":"+fmtHex(driver.M.Read16(control+callbackOffset)))
		}
		t.Logf("第 %2d 首 串流指標 %v", track+1, pointers)
		t.Logf("        回呼     %v", callbacks)
		t.Logf("        卡住     %v", result.Stuck)
	}
}

func fmtHex(v uint16) string {
	const digits = "0123456789ABCDEF"
	return string([]byte{digits[v>>12&15], digits[v>>8&15], digits[v>>4&15], digits[v&15]})
}

// 端到端：跑驅動、抽資料、合成，每一首都要出得了聲。
// **非靜音是判準**：合成器接錯的症狀是安靜，而安靜看起來像「這首本來就沒聲音」。
func TestEveryTrackRendersAudibly(t *testing.T) {
	image := original(t)
	for track := 0; track < 15; track++ {
		driver, err := Load(image, 6)
		if err != nil {
			t.Fatal(err)
		}
		result, err := driver.Play(track, 96)
		if err != nil {
			t.Fatalf("第 %d 首：%v", track+1, err)
		}
		events, unknown, err := result.Events(3)
		if err != nil {
			t.Fatalf("第 %d 首：%v", track+1, err)
		}
		samples, seconds, err := Render(events, result.Data, 3, RenderOptions{MaxSeconds: 8})
		if err != nil {
			t.Fatalf("第 %d 首：%v", track+1, err)
		}
		var loud int
		for _, s := range samples {
			v := int(s)
			if v < 0 {
				v = -v
			}
			if v > loud {
				loud = v
			}
		}
		if loud < 500 {
			t.Errorf("第 %d 首合成出來的峰值只有 %d，等於沒聲音", track+1, loud)
		}
		t.Logf("第 %2d 首：%.1f 秒、峰值 %5d、事件 %d、未解命令 %v",
			track+1, seconds, loud, len(events), unknown)
	}
}
