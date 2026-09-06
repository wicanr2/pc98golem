// Package disk 是磁碟容器的共同介面。
//
// PC-98 的軟碟映像有好幾種容器（VFD、D88…），裝的都是同一種 2HD 軟碟。
// 上面的 FAT12 那一層不該知道自己讀的是哪一種，所以容器只要滿足 [Image]。
package disk

import "errors"

// ErrAbsent 代表那個磁區在映像裡標成讀不到。
//
// **這個錯誤要一路傳到上層，不要吞掉**：靜靜地補零會讓「映像不完整」
// 看起來像「檔案沒問題」。
var ErrAbsent = errors.New("這個磁區在映像裡標成讀不到")

// Image 是一份開好的磁碟映像。
type Image interface {
	// Sector 讀一個邏輯磁區（LBA 從 0 起）。標成讀不到的磁區回包著
	// [ErrAbsent] 的錯誤。
	Sector(lba int) ([]byte, error)
	// SectorSize 是 LBA 0 的大小；PC-98 2HD 是 1024。
	SectorSize() int
	// Count 是讀得到的磁區數。
	Count() int
	// Absent 列出讀不到的磁區，由小到大。
	Absent() []int
}
