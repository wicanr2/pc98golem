package oracle

// Phys 把 20 位元線性位址包成 Addr。
func Phys(linear uint32) Addr {
	return Addr{Seg: uint16(linear >> 4), Off: uint16(linear & 0xF)}
}

// ---- 寫入 ----------------------------------------------------------------
//
// ⚠ **改被觀測程式的記憶體會讓後面的執行偏離原本的軌跡。**
//
// 正當用途只有一種：**做對照實驗**——Save、改一個值、觀察、Restore。
// 拿它「修正」程式的行為就是在偽造 oracle，那樣對拍出來的東西沒有意義。

// WriteU8／WriteU16 寫被觀測程式的記憶體。
//
// 名字不叫 WriteByte 是因為那個名字被 io.ByteWriter 佔了，
// 簽章不同會被 vet 擋下來。
func (o *Oracle) WriteU8(a Addr, v uint8)   { o.m.Write8(a.Linear(), v) }
func (o *Oracle) WriteU16(a Addr, v uint16) { o.m.Write16(a.Linear(), v) }
