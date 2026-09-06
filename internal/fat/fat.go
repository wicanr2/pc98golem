// Package fat 讀 PC-98 磁碟上的 FAT12。
//
// 疊在 [disk.Image] 上面（VFD 或 D88 都可以），一樣不需要 CPU 就能自己證明
// 自己對：讀出目錄、把檔案取出來，看它的開頭是不是該有的樣子
// （`MZ`、`TPOV`…）。
//
// # 壞磁區不補零就算了事
//
// 檔案跨到讀不到的磁區時，[Volume.Read] **一定要回錯**。靜靜地補零會讓
// 「這片映像不完整」看起來像「檔案沒問題」——手上這兩片 CoAB PC-98 映像
// 就各有壞磁區，打到的正好是 `MSCDRV.EXE`（音樂驅動）與 `CED3.DAX`。
package fat

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"github.com/wicanr2/pc98golem/internal/disk"
)

// Entry 是根目錄裡的一項。
type Entry struct {
	Name    string // 8.3，已經去掉填充空白
	Size    int
	Cluster int
	// Damaged 為真代表這個檔案跨到映像裡讀不到的磁區。
	Damaged bool
	// DamagedClusters 是那幾個叢集。
	DamagedClusters []int
}

// Geometry 說明版面是怎麼來的。
type Geometry string

const (
	// FromBootSector：sector 0 的 BPB 讀得出來，版面照它。
	FromBootSector Geometry = "boot-sector"
	// AssumedPC98_2HD：sector 0 沒有合法 BPB（例如自訂 IPL），
	// 版面用 PC-98 2HD 的標準值。**那是假設，不是讀出來的**——
	// CoAB 的 Disk 2 就是這種：sector 0 是帶 "AD&D -Curse of Th" 字樣的
	// 開機碼，BPB 欄位是垃圾，但版面與 Disk 1 相同。
	AssumedPC98_2HD Geometry = "assumed-pc98-2hd"
)

// Volume 是掛好的檔案系統。
type Volume struct {
	// Geometry 記著版面是讀出來的還是假設的。上層要據實轉述。
	Geometry Geometry

	image        disk.Image
	bytesPerSec  int
	secPerClus   int
	reserved     int
	fatCount     int
	rootEntries  int
	secPerFAT    int
	fat          []byte
	root         []byte
	dataFirstLBA int
}

// Mount 讀開機磁區的 BPB，把 FAT 與根目錄載進來。
func Mount(image disk.Image) (*Volume, error) {
	boot, err := image.Sector(0)
	if err != nil {
		return nil, fmt.Errorf("讀開機磁區：%w", err)
	}
	if len(boot) < 32 {
		return nil, errors.New("開機磁區太短")
	}
	volume := &Volume{
		Geometry:    FromBootSector,
		image:       image,
		bytesPerSec: int(binary.LittleEndian.Uint16(boot[11:13])),
		secPerClus:  int(boot[13]),
		reserved:    int(binary.LittleEndian.Uint16(boot[14:16])),
		fatCount:    int(boot[16]),
		rootEntries: int(binary.LittleEndian.Uint16(boot[17:19])),
		secPerFAT:   int(binary.LittleEndian.Uint16(boot[22:24])),
	}
	if !volume.plausible(image.SectorSize()) {
		// 沒有合法 BPB：退回 PC-98 2HD 的標準版面，並且記下這是假設。
		volume.Geometry = AssumedPC98_2HD
		volume.bytesPerSec = image.SectorSize()
		volume.secPerClus, volume.reserved = 1, 1
		volume.fatCount, volume.rootEntries, volume.secPerFAT = 2, 192, 2
		if !volume.plausible(image.SectorSize()) {
			return nil, fmt.Errorf("BPB 不合理，退回 PC-98 2HD 標準版面也不成立")
		}
	}
	rootSectors := volume.rootEntries * 32 / volume.bytesPerSec
	volume.fat, err = volume.readSectors(volume.reserved, volume.secPerFAT)
	if err != nil {
		return nil, fmt.Errorf("讀 FAT：%w", err)
	}
	rootLBA := volume.reserved + volume.fatCount*volume.secPerFAT
	volume.root, err = volume.readSectors(rootLBA, rootSectors)
	if err != nil {
		return nil, fmt.Errorf("讀根目錄：%w", err)
	}
	volume.dataFirstLBA = rootLBA + rootSectors
	return volume, nil
}

// plausible 判斷這組 BPB 值講不講得通。**不要只看非零**——CoAB 的 Disk 2
// 那組垃圾值裡每一項都非零（每磁區 49294、保留 1208），只有整組一起看才擋得住。
func (v *Volume) plausible(sectorSize int) bool {
	if v.bytesPerSec != sectorSize || v.secPerClus == 0 || v.secPerClus > 64 {
		return false
	}
	if v.reserved < 1 || v.reserved > 16 || v.fatCount < 1 || v.fatCount > 2 {
		return false
	}
	if v.rootEntries == 0 || v.rootEntries*32%v.bytesPerSec != 0 {
		return false
	}
	return v.secPerFAT >= 1 && v.secPerFAT <= 16
}

func (v *Volume) readSectors(first, count int) ([]byte, error) {
	out := make([]byte, 0, count*v.bytesPerSec)
	for i := 0; i < count; i++ {
		sector, err := v.image.Sector(first + i)
		if err != nil {
			return nil, err
		}
		out = append(out, sector...)
	}
	return out, nil
}

func (v *Volume) next(cluster int) int {
	index := cluster * 3 / 2
	if index+1 >= len(v.fat) {
		return 0xFFF
	}
	value := int(v.fat[index]) | int(v.fat[index+1])<<8
	if cluster&1 == 1 {
		return value >> 4
	}
	return value & 0xFFF
}

// List 列出根目錄裡的檔案（跳過已刪除的與磁碟標籤）。
func (v *Volume) List() []Entry {
	var out []Entry
	for offset := 0; offset+32 <= len(v.root); offset += 32 {
		record := v.root[offset : offset+32]
		if record[0] == 0x00 || record[0] == 0xE5 || record[11]&0x08 != 0 {
			continue
		}
		name := strings.TrimRight(string(record[:8]), " ")
		if ext := strings.TrimRight(string(record[8:11]), " "); ext != "" {
			name += "." + ext
		}
		entry := Entry{
			Name:    name,
			Cluster: int(binary.LittleEndian.Uint16(record[26:28])),
			Size:    int(binary.LittleEndian.Uint32(record[28:32])),
		}
		entry.DamagedClusters = v.damagedClusters(entry.Cluster, entry.Size)
		entry.Damaged = len(entry.DamagedClusters) > 0
		out = append(out, entry)
	}
	return out
}

// damagedClusters 走一次叢集鏈，回報哪幾個落在讀不到的磁區上。
func (v *Volume) damagedClusters(cluster, size int) []int {
	var bad []int
	read := 0
	for cluster >= 2 && cluster < 0xFF0 && read < size {
		for i := 0; i < v.secPerClus; i++ {
			if _, err := v.image.Sector(v.dataFirstLBA + (cluster-2)*v.secPerClus + i); errors.Is(err, disk.ErrAbsent) {
				bad = append(bad, cluster)
				break
			}
		}
		read += v.secPerClus * v.bytesPerSec
		cluster = v.next(cluster)
	}
	return bad
}

// Read 把一個檔案取出來。跨到讀不到的磁區就回錯——**不補零**。
func (v *Volume) Read(name string) ([]byte, error) {
	for _, entry := range v.List() {
		if !strings.EqualFold(entry.Name, name) {
			continue
		}
		if entry.Damaged {
			return nil, fmt.Errorf("%s 跨到讀不到的磁區（叢集 %v）：這份映像不完整，%w",
				entry.Name, entry.DamagedClusters, disk.ErrAbsent)
		}
		out := make([]byte, 0, entry.Size)
		cluster := entry.Cluster
		for cluster >= 2 && cluster < 0xFF0 && len(out) < entry.Size {
			for i := 0; i < v.secPerClus; i++ {
				sector, err := v.image.Sector(v.dataFirstLBA + (cluster-2)*v.secPerClus + i)
				if err != nil {
					return nil, err
				}
				out = append(out, sector...)
			}
			cluster = v.next(cluster)
		}
		if len(out) < entry.Size {
			return nil, fmt.Errorf("%s 只讀到 %d／%d 位元組：叢集鏈提早結束",
				entry.Name, len(out), entry.Size)
		}
		return out[:entry.Size], nil
	}
	return nil, fmt.Errorf("找不到 %s", name)
}
