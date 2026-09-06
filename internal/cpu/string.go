package cpu

// 字串指令（A4–AF）。
//
// 三條要照抄的 8086 行為：
//
//  1. **來源段可以被前綴改，目的段不行。** `MOVS` 的來源是 DS:SI（可覆寫）、
//     目的永遠是 ES:DI。`STOS`／`SCAS` 的目的也永遠是 ES:DI。
//     寫錯不會報錯，只會在有 `ES` 前綴的程式裡搬錯資料。
//  2. **`REP` 先看 CX。** CX ＝ 0 時整道指令什麼都不做，連一次都不跑。
//  3. **`REPE`／`REPNE` 只對 `CMPS` 與 `SCAS` 有意義**；其餘字串指令兩種前綴
//     都只是「重複 CX 次」。

// delta 回報這一次要往前還是往後，DF 決定。
func (c *CPU) delta(size int) uint16 {
	step := uint16(size / 8)
	if c.Flag(DF) {
		return -step
	}
	return step
}

func (c *CPU) stringOp(op uint8) {
	wide := op&1 == 1
	size := 8
	if wide {
		size = 16
	}
	d := c.delta(size)

	// 沒有 REP 前綴就只做一次。
	if c.repPrefix == 0 {
		c.stringOnce(op, wide, d)
		return
	}

	// REP：先看 CX（第 2 條）。
	checkZF := op == 0xA6 || op == 0xA7 || op == 0xAE || op == 0xAF
	wantZF := c.repPrefix == 0xF3
	for c.R[CX] != 0 {
		c.stringOnce(op, wide, d)
		c.R[CX]--
		if checkZF && c.Flag(ZF) != wantZF {
			break
		}
	}
}

func (c *CPU) stringOnce(op uint8, wide bool, d uint16) {
	src := c.dataSeg(DS) // 可被段前綴覆寫
	dst := c.Seg[ES]     // **不可覆寫**
	switch op {
	case 0xA4, 0xA5: // MOVS
		if wide {
			c.write16(dst, c.R[DI], c.read16(src, c.R[SI]))
		} else {
			c.write8(dst, c.R[DI], c.read8(src, c.R[SI]))
		}
		c.R[SI] += d
		c.R[DI] += d
	case 0xA6, 0xA7: // CMPS：比的是 [SI] − [DI]
		if wide {
			c.sub(uint32(c.read16(src, c.R[SI])), uint32(c.read16(dst, c.R[DI])), 0, 16)
		} else {
			c.sub(uint32(c.read8(src, c.R[SI])), uint32(c.read8(dst, c.R[DI])), 0, 8)
		}
		c.R[SI] += d
		c.R[DI] += d
	case 0xAA, 0xAB: // STOS
		if wide {
			c.write16(dst, c.R[DI], c.R[AX])
		} else {
			c.write8(dst, c.R[DI], uint8(c.R[AX]))
		}
		c.R[DI] += d
	case 0xAC, 0xAD: // LODS
		if wide {
			c.R[AX] = c.read16(src, c.R[SI])
		} else {
			c.setReg8(0, c.read8(src, c.R[SI]))
		}
		c.R[SI] += d
	case 0xAE, 0xAF: // SCAS：比的是 AL/AX − [DI]
		if wide {
			c.sub(uint32(c.R[AX]), uint32(c.read16(dst, c.R[DI])), 0, 16)
		} else {
			c.sub(uint32(uint8(c.R[AX])), uint32(c.read8(dst, c.R[DI])), 0, 8)
		}
		c.R[DI] += d
	}
}
