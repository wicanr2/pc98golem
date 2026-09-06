package oracle

import "encoding/binary"

// 記憶體搜尋：「畫面上那個數字存在哪裡」。
//
// 這是把判準從像素換成變數的第一步——知道位址之後才能用 Word／Bytes 讀它。
// rich2 那邊很多陣列只知道**描述子位址**，不知道資料落在哪；
// 拿一個已知的值（開局現金 25000）搜一次就定出來了。

// Search 找出記憶體裡每一個出現 pattern 的線性位址。
//
// 只掃常規記憶體（0 到 MemTop 的段界），不含視訊記憶體與 BIOS——
// 那兩塊會有大量假命中。
func (o *Oracle) Search(pattern []byte) []uint32 {
	if len(pattern) == 0 {
		return nil
	}
	mem := o.m.Mem[:memTop()]
	var out []uint32
	for i := 0; i+len(pattern) <= len(mem); i++ {
		if mem[i] != pattern[0] {
			continue
		}
		match := true
		for j := 1; j < len(pattern); j++ {
			if mem[i+j] != pattern[j] {
				match = false
				break
			}
		}
		if match {
			out = append(out, uint32(i))
		}
	}
	return out
}

// SearchWord 找 16 位元值（小端）。
func (o *Oracle) SearchWord(v uint16) []uint32 {
	var b [2]byte
	binary.LittleEndian.PutUint16(b[:], v)
	return o.Search(b[:])
}

// SearchDWord 找 32 位元值（小端）。BASIC 的長整數欄位是這個寬度。
func (o *Oracle) SearchDWord(v uint32) []uint32 {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	return o.Search(b[:])
}

// SearchChanged 比對快照與現況，回傳所有變了的線性位址。
//
// **這是找「這個動作改了什麼」最直接的辦法。** 做一件事之前 Save()，
// 做完之後拿快照來比——差異就是那件事碰過的狀態。
//
// 會有雜訊（畫面緩衝、計時器、堆疊），所以通常要配合「做兩次不同的事、
// 取交集或差集」來收斂。
func (o *Oracle) SearchChanged(s *State) []uint32 {
	old := s.mem()
	cur := o.m.Mem[:memTop()]
	var out []uint32
	for i := 0; i < len(cur) && i < len(old); i++ {
		if old[i] != cur[i] {
			out = append(out, uint32(i))
		}
	}
	return out
}

func memTop() int { return 0xA0000 }
