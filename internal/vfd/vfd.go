// Package vfd 讀 PC-98 的 VFD 磁碟映像（Virtual98／T98 的 `.fdd`）。
//
// 這一層不需要 CPU、不需要原版程式碼就能自己證明自己對：拿真的映像讀出
// 開機磁區與目錄，對得上就是對得上（spec 001 §4）。
//
// # 版面（由真映像量出來，不是抄格式文件）
//
//	$0000  "VFD1.00" 加填充，共 220（$DC）位元組
//	$00DC  磁區表，每筆 12 位元組：
//	         +0 C   磁柱
//	         +1 H   磁頭
//	         +2 R   磁區號（1 起）
//	         +3 N   大小碼，位元組數 = 128 << N
//	         +4..7  旗標
//	         +8..11 這個磁區在檔案裡的偏移（小端 32 位元）
//	         C == $FF 代表這個槽沒用到——**每軌配的槽比實際磁區多**
//	         （量到 4162 槽 ÷ 77 磁柱 ÷ 2 面 ≈ 27 槽，實際每軌 8 個磁區），
//	         所以**不能碰到 $FF 就當表結束**。
//	         偏移是 $FFFFFFFF 代表**這個磁區當初讀不到**。
//
// # 讀不到的磁區要吵
//
// 手上這兩片 CoAB PC-98 映像各有讀不到的磁區（Disk 1 是 LBA 55 與 673）。
// 靜靜地補零會讓「檔案壞了」看起來像「檔案沒問題」——而那個檔案正好是
// `MSCDRV.EXE`（音樂驅動）。所以 [Image.Sector] 對這種磁區回 [ErrAbsent]，
// 由上層決定要不要繼續。
package vfd

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"sort"
)

const (
	headerSize = 0xDC
	entrySize  = 12
	// SectorsPerTrack 是 PC-98 2HD 每軌的磁區數；LBA 換算要用它。
	SectorsPerTrack = 8
	unusedSlot      = 0xFF
	absentOffset    = 0xFFFFFFFF
)

// ErrAbsent 代表那個磁區在映像裡標成讀不到。
var ErrAbsent = errors.New("這個磁區在映像裡標成讀不到")

// Image 是一份開好的映像。
type Image struct {
	raw     []byte
	sectors map[int]entry
	absent  map[int]bool
}

type entry struct {
	offset int
	size   int
}

// Open 讀一份 VFD 映像。
func Open(path string) (*Image, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(raw)
}

// Parse 從記憶體裡的映像內容建 Image。
func Parse(raw []byte) (*Image, error) {
	if len(raw) < headerSize || string(raw[:7]) != "VFD1.00" {
		return nil, fmt.Errorf("不是 VFD 映像（開頭應該是 VFD1.00）")
	}
	image := &Image{raw: raw, sectors: map[int]entry{}, absent: map[int]bool{}}
	// 資料從第一個有效磁區的偏移開始，磁區表就到那裡為止。
	limit := len(raw)
	for offset := headerSize; offset+entrySize <= limit; offset += entrySize {
		record := raw[offset : offset+entrySize]
		if record[0] == unusedSlot {
			continue // 沒用到的槽，不是表的結尾
		}
		cylinder, head, sector := int(record[0]), int(record[1]), int(record[2])
		if sector == 0 {
			continue // 終止項
		}
		size := 128 << record[3]
		at := binary.LittleEndian.Uint32(record[8:12])
		lba := (cylinder*2+head)*SectorsPerTrack + sector - 1
		if at == absentOffset {
			image.absent[lba] = true
			continue
		}
		if int(at) < limit {
			limit = int(at) // 表不會越過第一筆資料
		}
		if int(at)+size > len(raw) {
			return nil, fmt.Errorf("磁區 C%d/H%d/R%d 的偏移 $%X 超出檔案", cylinder, head, sector, at)
		}
		image.sectors[lba] = entry{offset: int(at), size: size}
	}
	if len(image.sectors) == 0 {
		return nil, fmt.Errorf("磁區表是空的")
	}
	return image, nil
}

// Sector 讀一個邏輯磁區。讀不到的磁區回 [ErrAbsent]。
func (i *Image) Sector(lba int) ([]byte, error) {
	if i.absent[lba] {
		return nil, fmt.Errorf("LBA %d：%w", lba, ErrAbsent)
	}
	found, ok := i.sectors[lba]
	if !ok {
		return nil, fmt.Errorf("LBA %d 不在磁區表裡", lba)
	}
	return i.raw[found.offset : found.offset+found.size], nil
}

// SectorSize 是第一個磁區的大小；PC-98 2HD 是 1024。
func (i *Image) SectorSize() int {
	if found, ok := i.sectors[0]; ok {
		return found.size
	}
	return 0
}

// Count 是讀得到的磁區數。
func (i *Image) Count() int { return len(i.sectors) }

// Absent 列出讀不到的磁區，由小到大。**上層要把它印出來**，
// 不要靜靜地補零：壞掉的檔案看起來會和好的一樣。
func (i *Image) Absent() []int {
	out := make([]int, 0, len(i.absent))
	for lba := range i.absent {
		out = append(out, lba)
	}
	sort.Ints(out)
	return out
}
