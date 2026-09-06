package fat

import (
	"strings"
	"testing"
)

// pool 是 PC-98 版《Pool of Radiance》（Pony Canyon 1989-12-21）的映像，
// 容器是 D88。
func pool(t *testing.T, which string) *Volume {
	t.Helper()
	return mount(t, "PC98_POOL_DISK_DIR", which)
}

// spec 004 §「手上這兩份的實際形狀」：兩片都是 77 磁柱 × 2 面 × 8 磁區，
// 一個讀不到的磁區都沒有。**這一條要先成立**，後面「檔案讀對了」才有意義——
// 反過來會踩 spec 002 §3 那個坑（前幾個檔案讀對了證明不了版面對）。
func TestPoolD88HasNoAbsentSectors(t *testing.T) {
	for _, which := range []string{"Disk A", "Disk B"} {
		volume := pool(t, which)
		if got := volume.image.Count(); got != 77*2*8 {
			t.Errorf("%s 讀得到 %d 個磁區，預期 %d", which, got, 77*2*8)
		}
		if absent := volume.image.Absent(); len(absent) > 0 {
			t.Errorf("%s 有讀不到的磁區：%v", which, absent)
		}
		if got := volume.image.SectorSize(); got != 1024 {
			t.Errorf("%s 每磁區 %d 位元組，預期 1024", which, got)
		}
	}
}

// Disk A 的 BPB 是讀出來的，不是假設的（spec 004 §開機磁區）。
func TestPoolDiskAGeometryComesFromTheBootSector(t *testing.T) {
	volume := pool(t, "Disk A")
	if volume.Geometry != FromBootSector {
		t.Errorf("版面來源是 %q，預期 %q", volume.Geometry, FromBootSector)
	}
}

// 讀對了的判準：每個檔案都讀得完，長度與目錄項一致。
// 判準要涵蓋**整片**，不能只看前幾個檔案。
func TestPoolFileSizesMatchTheDirectory(t *testing.T) {
	for _, which := range []string{"Disk A", "Disk B"} {
		volume := pool(t, which)
		entries := volume.List()
		if len(entries) == 0 {
			t.Fatalf("%s 根目錄是空的", which)
		}
		for _, entry := range entries {
			body, err := volume.Read(entry.Name)
			if err != nil {
				t.Errorf("%s／%s：%v", which, entry.Name, err)
				continue
			}
			if len(body) != entry.Size {
				t.Errorf("%s／%s 讀出 %d 位元組，目錄項寫 %d",
					which, entry.Name, len(body), entry.Size)
			}
		}
		t.Logf("%s：%d 個檔案全部讀完", which, len(entries))
	}
}

// 執行檔的開頭要是 `MZ`——叢集鏈走錯的話這裡就露餡。
func TestPoolExecutablesLookLikeExecutables(t *testing.T) {
	volume := pool(t, "Disk A")
	var checked int
	for _, entry := range volume.List() {
		if !strings.HasSuffix(strings.ToUpper(entry.Name), ".EXE") {
			continue
		}
		body, err := volume.Read(entry.Name)
		if err != nil {
			t.Errorf("%s：%v", entry.Name, err)
			continue
		}
		if !strings.HasPrefix(string(body), "MZ") {
			t.Errorf("%s 開頭是 %q，預期 MZ", entry.Name, body[:2])
		}
		checked++
	}
	if checked == 0 {
		t.Error("Disk A 一個 .EXE 都沒有：目錄大概沒讀對")
	}
}
