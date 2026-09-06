// Package d88 讀 PC-98 的 D88 磁碟映像（Neko Project II 等模擬器的原生格式）。
//
// 與 [vfd] 是同一層的兩種容器，都滿足 [disk.Image]；版面與量測見
// `docs/spec/004-d88-container.md`。
//
// # 版面（由真映像量出來，不是抄格式文件）
//
//	$0000  名稱，17 位元組 NUL 結尾
//	$0011  保留 9 位元組
//	$001A  防寫旗標
//	$001B  磁碟類型（$20 = 2HD）
//	$001C  磁碟總長度，小端 32 位元
//	$0020  軌表：164 筆小端 32 位元偏移，0 = 這一軌沒有資料
//	$02B0  第一軌的資料
//
// 每一軌是連續的磁區，每個磁區前 16 位元組是標頭：C、H、R、N、
// 本軌磁區數（+4，小端 16 位元，每個磁區都重複一次）、density、deleted、
// status（+8，$00 = 讀得到）、保留 5 位元組、資料長度（+14，小端 16 位元）。
//
// # 走訪要用資料長度，不是 N
//
// 下一個磁區的位置是「這個磁區的資料長度」加出來的，不是 `128 << N`。
// 正常映像上兩者相同，但保護軌會故意讓它們不一致；用 N 推位置會從那一軌
// 之後整個錯開，而症狀只是「檔案內容怪怪的」——分不出是容器讀錯還是
// 檔案本來就這樣。
//
// # LBA 用整片一致的每軌磁區數
//
// 換算 LBA 的每軌磁區數取**第 0 軌**的磁區數，整片共用。不能用「這一軌自己的
// 磁區數」——防拷軌的磁區數本來就跟別人不一樣，拿它去乘會讓那一軌之後的
// LBA 全部偏掉。檔案系統看到的幾何本來就只有一種（BPB 裡那一組）。
package d88

import (
	"encoding/binary"
	"fmt"
	"os"
	"sort"

	"github.com/wicanr2/pc98golem/internal/disk"
)

const (
	headerSize   = 0x2B0
	trackCount   = 164
	trackTableAt = 0x20
	sectorHeader = 16
	statusOK     = 0x00
)

// Image 是一份開好的 D88 映像。
type Image struct {
	// Name 是映像自帶的名稱欄位（手上兩份都是 "New FD Image"）。
	Name string
	// DiskType 是 $001B；$20 代表 2HD。
	DiskType byte

	raw      []byte
	sectors  map[int]span
	absent   map[int]bool
	perTrack int
}

type span struct {
	offset int
	size   int
}

// physical 是走訪一軌得到的磁區，還沒換算成 LBA。
type physical struct {
	cylinder, head, sector int
	status                 byte
	offset, size           int
}

// Open 讀一份 D88 映像。
func Open(path string) (*Image, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(raw)
}

// Looks 判斷這串位元組像不像 D88。**D88 沒有魔數**，只能看版面講不講得通：
// 宣告長度要等於實際長度，軌表的偏移要落在檔案裡而且不小於標頭。
func Looks(raw []byte) bool {
	if len(raw) < headerSize {
		return false
	}
	if declared := binary.LittleEndian.Uint32(raw[0x1C:0x20]); int(declared) != len(raw) {
		return false
	}
	for i := 0; i < trackCount; i++ {
		at := binary.LittleEndian.Uint32(raw[trackTableAt+i*4 : trackTableAt+i*4+4])
		if at == 0 {
			continue
		}
		if int(at) < headerSize || int(at)+sectorHeader > len(raw) {
			return false
		}
	}
	return true
}

// Parse 從記憶體裡的映像內容建 Image。
func Parse(raw []byte) (*Image, error) {
	if len(raw) < headerSize {
		return nil, fmt.Errorf("不像 D88 映像：只有 %d 位元組，連標頭（%d）都不夠", len(raw), headerSize)
	}
	declared := int(binary.LittleEndian.Uint32(raw[0x1C:0x20]))
	if declared != len(raw) {
		return nil, fmt.Errorf("不像 D88 映像：標頭宣告 %d 位元組，實際 %d", declared, len(raw))
	}
	image := &Image{
		Name:     cstring(raw[:17]),
		DiskType: raw[0x1B],
		raw:      raw,
		sectors:  map[int]span{},
		absent:   map[int]bool{},
	}

	tracks := make([][]physical, trackCount)
	for track := 0; track < trackCount; track++ {
		at := int(binary.LittleEndian.Uint32(raw[trackTableAt+track*4 : trackTableAt+track*4+4]))
		if at == 0 {
			continue // 這一軌沒有資料
		}
		found, err := image.readTrack(track, at)
		if err != nil {
			return nil, err
		}
		tracks[track] = found
	}

	// 幾何取第 0 軌；那是 BPB 與 DOS 看到的那一組。
	image.perTrack = len(tracks[0])
	if image.perTrack == 0 {
		return nil, fmt.Errorf("第 0 軌沒有磁區：這份映像沒有可用的幾何")
	}
	for _, found := range tracks {
		for _, s := range found {
			lba := (s.cylinder*2+s.head)*image.perTrack + s.sector - 1
			if s.sector < 1 || lba < 0 {
				continue // 磁區號 0（有些保護軌會這樣）不進 LBA 空間
			}
			if s.status != statusOK {
				image.absent[lba] = true
				continue
			}
			image.sectors[lba] = span{offset: s.offset, size: s.size}
		}
	}
	if len(image.sectors) == 0 {
		return nil, fmt.Errorf("整片沒有讀得到的磁區")
	}
	return image, nil
}

// readTrack 走一整軌。磁區數只信**第一個**磁區標頭的 +4；後面的磁區就算
// 寫了不一樣的值也照走，保護軌會這樣（spec 004 契約 5）。
func (i *Image) readTrack(track, at int) ([]physical, error) {
	if at+sectorHeader > len(i.raw) {
		return nil, fmt.Errorf("軌 %d 的偏移 $%X 超出檔案", track, at)
	}
	count := int(binary.LittleEndian.Uint16(i.raw[at+4 : at+6]))
	if count == 0 {
		return nil, nil // 標成沒有磁區的軌
	}
	out := make([]physical, 0, count)
	offset := at
	for n := 0; n < count; n++ {
		if offset+sectorHeader > len(i.raw) {
			return nil, fmt.Errorf("軌 %d 第 %d 個磁區的標頭超出檔案", track, n)
		}
		head := i.raw[offset : offset+sectorHeader]
		size := int(binary.LittleEndian.Uint16(head[14:16]))
		if offset+sectorHeader+size > len(i.raw) {
			return nil, fmt.Errorf("軌 %d C%d/H%d/R%d 的資料（%d 位元組）超出檔案",
				track, head[0], head[1], head[2], size)
		}
		out = append(out, physical{
			cylinder: int(head[0]), head: int(head[1]), sector: int(head[2]),
			status: head[8], offset: offset + sectorHeader, size: size,
		})
		offset += sectorHeader + size
	}
	return out, nil
}

// Sector 讀一個邏輯磁區。讀不到的磁區回 [disk.ErrAbsent]。
func (i *Image) Sector(lba int) ([]byte, error) {
	if i.absent[lba] {
		return nil, fmt.Errorf("LBA %d：%w", lba, disk.ErrAbsent)
	}
	found, ok := i.sectors[lba]
	if !ok {
		return nil, fmt.Errorf("LBA %d 不在軌表裡", lba)
	}
	return i.raw[found.offset : found.offset+found.size], nil
}

// SectorSize 是 LBA 0 的大小；PC-98 2HD 是 1024。
func (i *Image) SectorSize() int {
	if found, ok := i.sectors[0]; ok {
		return found.size
	}
	return 0
}

// Count 是讀得到的磁區數。
func (i *Image) Count() int { return len(i.sectors) }

// SectorsPerTrack 是換算 LBA 用的每軌磁區數（取自第 0 軌）。
func (i *Image) SectorsPerTrack() int { return i.perTrack }

// Absent 列出讀不到的磁區（status 不是 $00），由小到大。
func (i *Image) Absent() []int {
	out := make([]int, 0, len(i.absent))
	for lba := range i.absent {
		out = append(out, lba)
	}
	sort.Ints(out)
	return out
}

func cstring(raw []byte) string {
	for n, b := range raw {
		if b == 0 {
			return string(raw[:n])
		}
	}
	return string(raw)
}
