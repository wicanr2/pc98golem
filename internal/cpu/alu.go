package cpu

import "math/bits"

// 算術／邏輯運算與旗標。規格在 `docs/spec/002` §3.1。
//
// 每一個運算都是「算結果 → 設旗標」兩步分開寫，不共用一支泛型的旗標函式——
// 8086 各運算對 CF／OF／AF 的定義**不一樣**，共用會讓其中幾個安靜地錯。

// parity 是低位元組裡 1 的個數為偶數。**只看低 8 位元**，16 位元運算也一樣。
func parity(v uint16) bool { return bits.OnesCount8(uint8(v))%2 == 0 }

// setSZP 設 SF／ZF／PF。size 是 8 或 16。
func (c *CPU) setSZP(v uint16, size int) {
	if size == 8 {
		c.setFlag(SF, v&0x80 != 0)
		c.setFlag(ZF, uint8(v) == 0)
	} else {
		c.setFlag(SF, v&0x8000 != 0)
		c.setFlag(ZF, v == 0)
	}
	c.setFlag(PF, parity(v))
}

// signBit 是該寬度的符號位。
func signBit(size int) uint32 {
	if size == 8 {
		return 0x80
	}
	return 0x8000
}

func maskOf(size int) uint32 {
	if size == 8 {
		return 0xFF
	}
	return 0xFFFF
}

// add 是 ADD／ADC 共用的本體。carry 帶 0 或 1。
func (c *CPU) add(a, b uint32, carry uint32, size int) uint16 {
	m, sb := maskOf(size), signBit(size)
	full := a + b + carry
	res := full & m
	c.setFlag(CF, full&^m != 0)
	// AF 是 bit 3 的進位——**只看低 4 位元**，與寬度無關。
	c.setFlag(AF, (a&0xF)+(b&0xF)+carry > 0xF)
	// OF：兩個運算元同號、結果異號。
	c.setFlag(OF, (a^res)&(b^res)&sb != 0)
	c.setSZP(uint16(res), size)
	return uint16(res)
}

// sub 是 SUB／SBB／CMP 共用的本體。borrow 帶 0 或 1。
func (c *CPU) sub(a, b uint32, borrow uint32, size int) uint16 {
	m, sb := maskOf(size), signBit(size)
	full := a - b - borrow
	res := full & m
	c.setFlag(CF, full&^m != 0)
	c.setFlag(AF, (a&0xF) < (b&0xF)+borrow)
	// OF：兩運算元異號、且結果與**被減數**異號。
	c.setFlag(OF, (a^b)&(a^res)&sb != 0)
	c.setSZP(uint16(res), size)
	return uint16(res)
}

// logic 是 AND／OR／XOR／TEST 的旗標：CF 與 OF 清零，AF 未定義。
//
// **AF 這裡明確寫 0**，不是因為 8086 保證，是因為要有一個確定的值；
// SingleStepTests 會用 metadata 的 flags-mask 把它遮掉（`docs/spec/002` §5）。
func (c *CPU) logic(res uint16, size int) uint16 {
	c.setFlag(CF, false)
	c.setFlag(OF, false)
	c.setFlag(AF, false)
	c.setSZP(res, size)
	return res
}

func (c *CPU) inc(v uint16, size int) uint16 {
	cf := c.Flag(CF) // INC 不動 CF
	r := c.add(uint32(v), 1, 0, size)
	c.setFlag(CF, cf)
	return r
}

func (c *CPU) dec(v uint16, size int) uint16 {
	cf := c.Flag(CF) // DEC 不動 CF
	r := c.sub(uint32(v), 1, 0, size)
	c.setFlag(CF, cf)
	return r
}

func (c *CPU) neg(v uint16, size int) uint16 {
	m, sb := maskOf(size), signBit(size)
	res := (-uint32(v)) & m
	c.setFlag(CF, uint32(v)&m != 0)
	c.setFlag(AF, uint32(v)&0xF != 0)
	// OF 只在「運算元就是符號位最小值」時成立——那個值取負會是自己。
	c.setFlag(OF, uint32(v)&m == sb)
	c.setSZP(uint16(res), size)
	return uint16(res)
}

// ---- 移位與旋轉（`docs/spec/002` §3.2）----------------------------------
//
// 兩條 8086 專屬的行為要照做：
//
//  1. **位移量不遮罩成 5 位元**（286 以上才遮）。CL=32 時值會被移光。
//  2. **CL ＝ 0 時完全不動旗標**，連 ZF 都不動。
//
//  3. **OF 每一圈都重算**，不是只在「移 1 位」時才有定義。手冊說移多位時
//     OF 未定義，但實機的微碼每一圈都做同樣的事，所以**最後一圈的值勝出**。
//     `D2.3`（RCR by CL）的 flags-mask 是 FFFF——**一個位元都沒遮**，
//     照「只有移 1 位才算 OF」寫會整批紅。

func (c *CPU) shiftRotate(op int, v uint16, count uint8, size int) uint16 {
	if count == 0 {
		return v
	}
	m := maskOf(size)
	sb := signBit(size)
	x := uint32(v) & m
	var last bool // 最後移出去的那一位，變成 CF

	// hi2 是「最高兩位相異」，ROR／RCR 的 OF 就是它。
	hi2 := func() bool { return (x&sb != 0) != (x&(sb>>1) != 0) }

	switch op {
	case 0: // ROL
		for i := uint8(0); i < count; i++ {
			hi := x & sb
			x = (x << 1) & m
			if hi != 0 {
				x |= 1
			}
			last = hi != 0
			c.setFlag(OF, (x&sb != 0) != last)
		}
		c.setFlag(CF, last)
	case 1: // ROR
		for i := uint8(0); i < count; i++ {
			lo := x & 1
			x >>= 1
			if lo != 0 {
				x |= sb
			}
			last = lo != 0
			c.setFlag(OF, hi2())
		}
		c.setFlag(CF, last)
	case 2: // RCL
		cf := uint32(0)
		if c.Flag(CF) {
			cf = 1
		}
		for i := uint8(0); i < count; i++ {
			hi := x & sb
			x = (x<<1)&m | cf
			cf = 0
			if hi != 0 {
				cf = 1
			}
			c.setFlag(OF, (x&sb != 0) != (cf != 0))
		}
		c.setFlag(CF, cf != 0)
	case 3: // RCR
		cf := uint32(0)
		if c.Flag(CF) {
			cf = 1
		}
		for i := uint8(0); i < count; i++ {
			lo := x & 1
			x >>= 1
			if cf != 0 {
				x |= sb
			}
			cf = lo
			c.setFlag(OF, hi2())
		}
		c.setFlag(CF, cf != 0)
	case 6: // SETMO／SETMOC——**未公開，而且不是 SHL 的別名**
		// 186 以上把 `/6` 當成 `/4`（SHL）的別名，**8086 不是**：
		// 它把目的地整個設成 1（`SETMO`；`D2`／`D3` 的版本 `SETMOC`
		// 在 CL ＝ 0 時什麼都不做，由上面那個 count == 0 的早退處理）。
		//
		// 語料：`D0.6`／`D1.6`／`D2.6`／`D3.6` 四個檔，指令名就叫 `setmo`。
		// 四個檔的 flags-mask 都是 F72A——**六個算術旗標全部被遮掉**，
		// 所以下面設的旗標是我們挑的確定值，不是實機保證。
		x = m
		c.setFlag(CF, false)
		c.setFlag(OF, false)
		c.setFlag(AF, false)
		c.setSZP(uint16(x), size)
		return uint16(x)
	case 4: // SHL／SAL
		for i := uint8(0); i < count; i++ {
			last = x&sb != 0
			x = (x << 1) & m
			c.setFlag(OF, (x&sb != 0) != last)
		}
		c.setFlag(CF, last)
		c.setSZP(uint16(x), size)
		c.setFlag(AF, false)
		return uint16(x)
	case 5: // SHR
		for i := uint8(0); i < count; i++ {
			// OF ＝ **這一圈移之前**的最高位。多位移時最後一圈勝出，
			// 所以不是「原始運算元的最高位」——那是只移 1 位時的特例。
			c.setFlag(OF, x&sb != 0)
			last = x&1 != 0
			x >>= 1
		}
		c.setFlag(CF, last)
		c.setSZP(uint16(x), size)
		c.setFlag(AF, false)
		return uint16(x)
	case 7: // SAR
		for i := uint8(0); i < count; i++ {
			last = x&1 != 0
			x = (x >> 1) | (x & sb)
		}
		c.setFlag(CF, last)
		c.setFlag(OF, false)
		c.setSZP(uint16(x), size)
		c.setFlag(AF, false)
		return uint16(x)
	}
	// 旋轉類**不動 SF／ZF／PF**——只有移位類會動。
	return uint16(x)
}

// ---- 乘除 ---------------------------------------------------------------

func (c *CPU) mul8(v uint8) {
	res := uint16(uint8(c.R[AX])) * uint16(v)
	c.R[AX] = res
	hi := res&0xFF00 != 0
	c.setFlag(CF, hi)
	c.setFlag(OF, hi)
	// 8086 的 MUL 會照結果設 SZP（手冊說未定義，實機有值）；
	// 遮罩交給 SingleStepTests 的 metadata。
	c.setSZP(res, 16)
	c.setFlag(ZF, res == 0)
}

func (c *CPU) mul16(v uint16) {
	res := uint32(c.R[AX]) * uint32(v)
	c.R[AX] = uint16(res)
	c.R[DX] = uint16(res >> 16)
	hi := c.R[DX] != 0
	c.setFlag(CF, hi)
	c.setFlag(OF, hi)
	c.setSZP(uint16(res>>16), 16)
	c.setFlag(ZF, res == 0)
}

func (c *CPU) imul8(v uint8) {
	res := int16(int8(uint8(c.R[AX]))) * int16(int8(v))
	c.R[AX] = uint16(res)
	// CF／OF ＝ 高半部不是低半部的符號延伸。
	sig := int16(int8(uint8(res))) != res
	c.setFlag(CF, sig)
	c.setFlag(OF, sig)
	c.setSZP(uint16(res), 16)
}

func (c *CPU) imul16(v uint16) {
	res := int32(int16(c.R[AX])) * int32(int16(v))
	c.R[AX] = uint16(res)
	c.R[DX] = uint16(uint32(res) >> 16)
	sig := int32(int16(uint16(res))) != res
	c.setFlag(CF, sig)
	c.setFlag(OF, sig)
	c.setSZP(uint16(uint32(res)>>16), 16)
}

// ---- 除法 ---------------------------------------------------------------
//
// 除法的旗標在手冊上是「全部未定義」，SingleStepTests 也把它們遮掉——
// **但溢位那條路會把旗標推上堆疊**（`INT 0`），而堆疊的比對是逐位元組的。
// 所以「未定義」在這裡不等於「隨便」。
//
// 從語料反推出來的規則（`docs/spec/002` §3.4）：
//
//	DIV：旗標 ＝ 內部第一次比較「被除數高半部 − 除數」留下來的
//	     F6.6 的 1,439 筆暫存器型溢位樣本全中，F7.6 的 1,372 筆也全中。
//
// **`IDIV` 還沒解**：它先把兩邊取絕對值再做無號除法，溢位是在迴圈中途
// 才偵測到的，所以旗標來自比較晚的一次內部減法。用同一條規則只對
// 約 65%（`docs/spec/002` §3.4 有數字）。現況：照 DIV 的規則寫，
// 並在測試裡以**上限計數**盯住，不讓它悄悄變差。

// divFlags 設「被除數高半部 − 除數」那一次比較的旗標。
func (c *CPU) divFlags(high, divisor uint32, size int) {
	c.sub(high, divisor, 0, size)
}

// div8／div16／idiv8／idiv16 回傳 false 表示要觸發 INT 0（除以 0 或商溢位）。
func (c *CPU) div8(v uint8) bool {
	c.divFlags(uint32(c.reg8(4)), uint32(v), 8)
	if v == 0 {
		return false
	}
	q := c.R[AX] / uint16(v)
	if q > 0xFF {
		return false
	}
	r := c.R[AX] % uint16(v)
	c.R[AX] = q&0xFF | r<<8
	return true
}

func (c *CPU) div16(v uint16) bool {
	c.divFlags(uint32(c.R[DX]), uint32(v), 16)
	if v == 0 {
		return false
	}
	n := uint32(c.R[DX])<<16 | uint32(c.R[AX])
	q := n / uint32(v)
	if q > 0xFFFF {
		return false
	}
	c.R[AX] = uint16(q)
	c.R[DX] = uint16(n % uint32(v))
	return true
}

func (c *CPU) idiv8(v uint8) bool {
	c.divFlags(uint32(c.reg8(4)), uint32(v), 8)
	if v == 0 {
		return false
	}
	n := int16(c.R[AX])
	d := int16(int8(v))
	q := n / d
	if q < -128 || q > 127 {
		return false
	}
	r := n % d
	c.R[AX] = uint16(uint8(int8(q))) | uint16(uint8(int8(r)))<<8
	return true
}

func (c *CPU) idiv16(v uint16) bool {
	c.divFlags(uint32(c.R[DX]), uint32(v), 16)
	if v == 0 {
		return false
	}
	n := int32(uint32(c.R[DX])<<16 | uint32(c.R[AX]))
	d := int32(int16(v))
	q := n / d
	if q < -32768 || q > 32767 {
		return false
	}
	c.R[AX] = uint16(int16(q))
	c.R[DX] = uint16(int16(n % d))
	return true
}

// ---- BCD 調整（`docs/spec/002` §3.3）------------------------------------

// bcdHighAdjust 是 DAA／DAS 第二段的條件。
//
// ⚠ **不是課本上的 `old_AL > 99h || old_CF`。** 實機在 `old_AL` 落在
// 9Ah–9Fh 而且**進來時 AF 已經是 1** 的那六個值上不做第二段調整。
// 這條規則是從 SingleStepTests 的 27／2F 兩個檔各 10,000 筆反推出來的
// （兩檔各 0 筆不合），不是從手冊抄的——手冊的版本在那六個值上是錯的。
func bcdHighAdjust(oldAL uint8, oldCF, oldAF bool) bool {
	return oldCF || oldAL > 0x9F || (oldAL > 0x99 && !oldAF)
}

func (c *CPU) daa() {
	old := uint8(c.R[AX])
	oldCF, oldAF := c.Flag(CF), c.Flag(AF)
	al := old
	if al&0x0F > 9 || oldAF {
		al += 6
		c.setFlag(AF, true)
	} else {
		c.setFlag(AF, false)
	}
	if bcdHighAdjust(old, oldCF, oldAF) {
		al += 0x60
		c.setFlag(CF, true)
	} else {
		c.setFlag(CF, false)
	}
	c.setReg8(0, al)
	c.setSZP(uint16(al), 8)
}

func (c *CPU) das() {
	old := uint8(c.R[AX])
	oldCF, oldAF := c.Flag(CF), c.Flag(AF)
	al := old
	if al&0x0F > 9 || oldAF {
		al -= 6
		c.setFlag(AF, true)
	} else {
		c.setFlag(AF, false)
	}
	if bcdHighAdjust(old, oldCF, oldAF) {
		al -= 0x60
		c.setFlag(CF, true)
	} else {
		c.setFlag(CF, false)
	}
	c.setReg8(0, al)
	c.setSZP(uint16(al), 8)
}

// AAA／AAS 的加減是**分開對 AL 與 AH 做的**，不是對整個 AX 加 106h——
// AL + 6 溢位時進位**不會**跑進 AH。課本寫成 `AX += 106h`，那在
// AL ≥ FAh 時會讓 AH 多加一次（SingleStepTests 的 37 檔會抓到）。
func (c *CPU) aaa() {
	al, ah := uint8(c.R[AX]), c.reg8(4)
	if al&0x0F > 9 || c.Flag(AF) {
		al += 6
		ah++
		c.setFlag(AF, true)
		c.setFlag(CF, true)
	} else {
		c.setFlag(AF, false)
		c.setFlag(CF, false)
	}
	c.setReg8(0, al&0x0F)
	c.setReg8(4, ah)
}

func (c *CPU) aas() {
	al, ah := uint8(c.R[AX]), c.reg8(4)
	if al&0x0F > 9 || c.Flag(AF) {
		al -= 6
		ah--
		c.setFlag(AF, true)
		c.setFlag(CF, true)
	} else {
		c.setFlag(AF, false)
		c.setFlag(CF, false)
	}
	c.setReg8(0, al&0x0F)
	c.setReg8(4, ah)
}
