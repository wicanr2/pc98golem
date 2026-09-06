package cpu

// 80186 新增的指令（`docs/spec/002` §1.1）。
//
// **只在 `Model >= Model80186` 時走這裡。** 這些 opcode 在 8086 上有別的
// 意思（`60`–`6F` 是條件跳躍的別名、`C0`／`C1` 是 `RET` 的別名、
// `C8`／`C9` 是 `RETF` 的別名），而且**指令長度不同**——選錯機型不會報錯，
// 只會從那一道開始整串錯位。

// op186_6x 處理 `60`–`6F`。
func (c *CPU) op186_6x(op uint8) error {
	switch op {
	case 0x60: // PUSHA
		// **推的是進來時的 SP**，不是遞減後的。
		sp := c.R[SP]
		for _, r := range [...]int{AX, CX, DX, BX} {
			c.push(c.R[r])
		}
		c.push(sp)
		for _, r := range [...]int{BP, SI, DI} {
			c.push(c.R[r])
		}

	case 0x61: // POPA
		// SP 那一格**丟掉不用**（真的寫回去就把堆疊指標打壞了）。
		for _, r := range [...]int{DI, SI, BP} {
			c.R[r] = c.pop()
		}
		c.pop()
		for _, r := range [...]int{BX, DX, CX, AX} {
			c.R[r] = c.pop()
		}

	case 0x62: // BOUND r16, m
		// m 指到兩個有號的界，含頭含尾。超出範圍發 INT 5。
		m := c.decodeModRM()
		idx := int16(c.R[m.reg])
		lo := int16(c.read16(m.rm.seg, m.rm.off))
		hi := int16(c.read16(m.rm.seg, m.rm.off+2))
		if idx < lo || idx > hi {
			// 和除法例外一樣，例外要回到**這一道指令的起點**
			// （`docs/spec/002` §4）。
			c.Seg[CS], c.IP = c.opCS, c.opIP
			c.Interrupt(5)
		}

	case 0x68: // PUSH imm16
		c.push(c.fetch16())

	case 0x6A: // PUSH imm8（**符號延伸**成 16 位元）
		c.push(uint16(int16(int8(c.fetch8()))))

	case 0x69, 0x6B: // IMUL r16, r/m16, imm
		m := c.decodeModRM()
		src := int32(int16(c.get16(m.rm)))
		var imm int32
		if op == 0x69 {
			imm = int32(int16(c.fetch16()))
		} else {
			imm = int32(int8(c.fetch8())) // 符號延伸
		}
		full := src * imm
		c.R[m.reg] = uint16(full)
		// CF ＝ OF ＝「截成 16 位元之後再符號延伸，不等於完整結果」。
		over := int32(int16(uint16(full))) != full
		c.setFlag(CF, over)
		c.setFlag(OF, over)

	case 0x6C, 0x6D, 0x6E, 0x6F: // INS／OUTS
		c.insOuts(op)

	default:
		return c.errf(op, "未實作的 80186 opcode")
	}
	return nil
}

// insOuts 是 `6C`–`6F`：字串版的 `in`／`out`。
//
// 這是 mode 13h 上傳調色盤的標準寫法（`mov dx,3C9h` ＋ `mov cx,768`
// ＋ `rep outsb`），所以 MVP-B 一定會走到。
//
// ⚠ **INS 的目的地是 `ES:DI`，而且段前綴覆寫不了**；
// OUTS 的來源是 `DS:SI`，段前綴**可以**覆寫。
func (c *CPU) insOuts(op uint8) {
	wide := op&1 == 1
	size := 8
	if wide {
		size = 16
	}
	d := c.delta(size)

	once := func() {
		switch op {
		case 0x6C: // INSB
			c.write8(c.Seg[ES], c.R[DI], c.Bus.In8(c.R[DX]))
			c.R[DI] += d
		case 0x6D: // INSW
			lo := uint16(c.Bus.In8(c.R[DX]))
			hi := uint16(c.Bus.In8(c.R[DX]))
			c.write16(c.Seg[ES], c.R[DI], lo|hi<<8)
			c.R[DI] += d
		case 0x6E: // OUTSB
			c.Bus.Out8(c.R[DX], c.read8(c.dataSeg(DS), c.R[SI]))
			c.R[SI] += d
		case 0x6F: // OUTSW
			v := c.read16(c.dataSeg(DS), c.R[SI])
			c.Bus.Out8(c.R[DX], uint8(v))
			c.Bus.Out8(c.R[DX], uint8(v>>8))
			c.R[SI] += d
		}
	}

	if c.repPrefix == 0 {
		once()
		return
	}
	// INS／OUTS 不看 ZF，所以 F2 與 F3 一樣，都是「做 CX 次」。
	for c.R[CX] != 0 {
		once()
		c.R[CX]--
	}
}

// shiftImm 是 `C0`／`C1`：移位／旋轉 imm8 次。
//
// 8086 沒有這兩個（那裡是 `RET` 的別名），所以移位次數只能放 CL。
func (c *CPU) shiftImm(op uint8) error {
	m := c.decodeModRM()
	count := c.fetch8()
	if op == 0xC0 {
		c.set8(m.rm, uint8(c.shiftRotate(m.reg, uint16(c.get8(m.rm)), count, 8)))
	} else {
		c.set16(m.rm, c.shiftRotate(m.reg, c.get16(m.rm), count, 16))
	}
	return nil
}

// enter 是 `C8`：建堆疊框，含巢狀層級。
func (c *CPU) enter() {
	alloc := c.fetch16()
	level := c.fetch8() & 0x1F
	c.push(c.R[BP])
	frame := c.R[SP]
	for i := uint8(1); i < level; i++ {
		c.R[BP] -= 2
		c.push(c.read16(c.Seg[SS], c.R[BP]))
	}
	if level > 0 {
		c.push(frame)
	}
	c.R[BP] = frame
	c.R[SP] -= alloc
}

// leave 是 `C9`：拆掉 enter 建的框。
func (c *CPU) leave() {
	c.R[SP] = c.R[BP]
	c.R[BP] = c.pop()
}
