package fat

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wicanr2/pc98golem/internal/d88"
	"github.com/wicanr2/pc98golem/internal/disk"
	"github.com/wicanr2/pc98golem/internal/vfd"
)

// 原版映像不進版控（spec 001 §5）。缺檔就 skip——**不做代用品**，
// 安靜的替代品會讓「還沒做完」看起來像做完了。
//
// 容器由內容判別，不看副檔名：同一片 2HD 軟碟裝成 VFD 或 D88 都是同一片。
func mount(t *testing.T, env, which string) *Volume {
	t.Helper()
	dir := os.Getenv(env)
	if dir == "" {
		t.Skipf("沒有 %s：原版映像不進版控", env)
	}
	var matches []string
	for _, ext := range []string{".fdd", ".d88"} {
		found, err := filepath.Glob(filepath.Join(dir, "*"+which+"*"+ext))
		if err == nil {
			matches = append(matches, found...)
		}
	}
	if len(matches) == 0 {
		t.Skipf("在 %s 找不到 %s 的映像", dir, which)
	}
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	var image disk.Image
	switch {
	case len(raw) >= 7 && string(raw[:7]) == "VFD1.00":
		image, err = vfd.Parse(raw)
	case d88.Looks(raw):
		image, err = d88.Parse(raw)
	default:
		t.Fatalf("%s 認不出容器", matches[0])
	}
	if err != nil {
		t.Fatal(err)
	}
	volume, err := Mount(image)
	if err != nil {
		t.Fatal(err)
	}
	return volume
}

// coab 是 CoAB 的 PC-98 映像（VFD）。
func coab(t *testing.T, which string) *Volume {
	t.Helper()
	return mount(t, "PC98_DISK_DIR", which)
}

// 讀對了的判準：取出來的檔案開頭要是它該有的樣子。
// 這一條不需要 CPU、不需要跑遊戲就能證明磁碟層是對的。
func TestCoABDiskOneFilesHaveTheRightHeaders(t *testing.T) {
	volume := coab(t, "Disk 1")
	want := map[string]string{
		"GAME.EXE":   "MZ",   // DOS 執行檔
		"SETUP.EXE":  "MZ",   //
		"GAME.OVR":   "TPOV", // Turbo Pascal overlay 容器
	}
	for name, prefix := range want {
		body, err := volume.Read(name)
		if err != nil {
			t.Errorf("%s：%v", name, err)
			continue
		}
		if !strings.HasPrefix(string(body), prefix) {
			t.Errorf("%s 開頭是 %q，預期 %q", name, body[:len(prefix)], prefix)
		}
	}
}

// 檔案大小要與目錄項一致——叢集鏈走錯的話這裡就對不上。
func TestCoABFileSizesMatchTheDirectory(t *testing.T) {
	volume := coab(t, "Disk 1")
	for _, entry := range volume.List() {
		if entry.Damaged {
			continue
		}
		body, err := volume.Read(entry.Name)
		if err != nil {
			t.Errorf("%s：%v", entry.Name, err)
			continue
		}
		if len(body) != entry.Size {
			t.Errorf("%s 讀出 %d 位元組，目錄項寫 %d", entry.Name, len(body), entry.Size)
		}
	}
}

// **壞磁區要吵。** 手上這片 Disk 1 有兩個讀不到的磁區，打到 MSCDRV.EXE。
// 讀它一定要回錯，不能補零假裝沒事——那個檔案是音樂驅動，
// 補零之後「壞掉」看起來會和「好的」一樣。
func TestCoABDamagedFileRefusesToReadInsteadOfZeroFilling(t *testing.T) {
	volume := coab(t, "Disk 1")
	var damaged []Entry
	for _, entry := range volume.List() {
		if entry.Damaged {
			damaged = append(damaged, entry)
		}
	}
	if len(damaged) == 0 {
		t.Skip("這片映像沒有壞磁區")
	}
	for _, entry := range damaged {
		body, err := volume.Read(entry.Name)
		if err == nil {
			t.Errorf("%s 跨到讀不到的磁區卻讀成功了（%d 位元組）", entry.Name, len(body))
			continue
		}
		if !errors.Is(err, disk.ErrAbsent) {
			t.Errorf("%s 的錯誤沒有包住 disk.ErrAbsent：%v", entry.Name, err)
		}
	}
	t.Logf("這片映像有 %d 個檔案跨到壞磁區：", len(damaged))
	for _, entry := range damaged {
		t.Logf("  %s（叢集 %v）", entry.Name, entry.DamagedClusters)
	}
}

// Disk 2 的 sector 0 是自訂 IPL（帶 "AD&D -Curse of Th" 字樣），沒有合法 BPB。
// 版面要退回 PC-98 2HD 的標準值，**而且要標明那是假設**。
func TestCoABDiskTwoFallsBackToAssumedGeometry(t *testing.T) {
	volume := coab(t, "Disk 2")
	if volume.Geometry != AssumedPC98_2HD {
		t.Errorf("版面來源是 %q，預期 %q", volume.Geometry, AssumedPC98_2HD)
	}
	entries := volume.List()
	if len(entries) < 18 {
		t.Fatalf("只列出 %d 個檔案，這片應該有十幾個 .DAX", len(entries))
	}
	// 遊戲資料都在這一片：ECL、GEO、PIC 這幾個是 remake 直接吃的。
	for _, name := range []string{"ECL.DAX", "GEO.DAX", "PIC.DAX"} {
		body, err := volume.Read(name)
		if err != nil {
			t.Errorf("%s：%v", name, err)
			continue
		}
		if len(body) == 0 {
			t.Errorf("%s 讀出 0 位元組", name)
		}
	}
}

// Disk 1 的版面是從 BPB 讀出來的，不是假設的——兩片不要混為一談。
func TestCoABDiskOneGeometryComesFromTheBootSector(t *testing.T) {
	volume := coab(t, "Disk 1")
	if volume.Geometry != FromBootSector {
		t.Errorf("版面來源是 %q，預期 %q", volume.Geometry, FromBootSector)
	}
}
