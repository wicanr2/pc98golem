package cpu

// 指令解碼與執行。對照 `docs/spec/002-cpu-8086.md`。

// Step 執行一道指令（含它的前綴）。
//
// 回傳非 nil 表示**執行不下去**（解不出來的 opcode）——不是程式自己出錯。
// 程式自己的例外（除以 0）走 INT 0，不回傳錯誤。
//
// ⚠ **時序不在 MVP-A 範圍**（`docs/spec/001` §4）。要等 M2 接 PIT 時才有
// 週期數，那時候會是另一支 API，不是在這裡偷偷回一個猜的數字。
func (c *CPU) Step() error {
	c.segOverride = noSegOverride
	c.repPrefix = 0
	c.lock = false
	c.opCS, c.opIP = c.Seg[CS], c.IP

	for {
		op := c.fetch8()
		switch op {
		case 0x26:
			c.segOverride = ES
			continue
		case 0x2E:
			c.segOverride = CS
			continue
		case 0x36:
			c.segOverride = SS
			continue
		case 0x3E:
			c.segOverride = DS
			continue
		case 0xF0, 0xF1: // LOCK（F1 在 8086 是它的別名）
			c.lock = true
			continue
		case 0xF2, 0xF3:
			c.repPrefix = op
			continue
		}
		return c.execute(op)
	}
}

//gocyclo:ignore
func (c *CPU) execute(op uint8) error {
	switch {
	// ---- 00–3F：八個 ALU 運算，每個六種定址 ----------------------------
	case op < 0x40 && op&7 < 6 && op != 0x0F && op != 0x27 && op != 0x2F &&
		op != 0x37 && op != 0x3F:
		return c.aluGroup(op)

	// ---- 段暫存器的 PUSH／POP -------------------------------------------
	case op == 0x06:
		c.push(c.Seg[ES])
	case op == 0x07:
		c.Seg[ES] = c.pop()
	case op == 0x0E:
		c.push(c.Seg[CS])
	case op == 0x0F:
		// **8086 的 0F 是 POP CS**，不是雙位元組前綴。
		c.Seg[CS] = c.pop()
	case op == 0x16:
		c.push(c.Seg[SS])
	case op == 0x17:
		c.Seg[SS] = c.pop()
	case op == 0x1E:
		c.push(c.Seg[DS])
	case op == 0x1F:
		c.Seg[DS] = c.pop()

	// ---- BCD 調整 --------------------------------------------------------
	case op == 0x27:
		c.daa()
	case op == 0x2F:
		c.das()
	case op == 0x37:
		c.aaa()
	case op == 0x3F:
		c.aas()

	// ---- 40–5F：INC／DEC／PUSH／POP 通用暫存器 --------------------------
	case op >= 0x40 && op <= 0x47:
		r := int(op - 0x40)
		c.R[r] = c.inc(c.R[r], 16)
	case op >= 0x48 && op <= 0x4F:
		r := int(op - 0x48)
		c.R[r] = c.dec(c.R[r], 16)
	case op >= 0x50 && op <= 0x57:
		c.pushOperand(regOperand(int(op - 0x50)))
	case op >= 0x58 && op <= 0x5F:
		c.R[op-0x58] = c.pop()

	// ---- 60–6F：機型相關（`docs/spec/002` §1.1）--------------------------
	//
	// 8086 把這一段當成 `70`–`7F` 的別名（語料驗證過）；80186 把它給了
	// `PUSHA` 那一族。**兩者的指令長度不同**，選錯不會報錯，只會錯位。
	case op >= 0x60 && op <= 0x6F && c.Model >= Model80186:
		return c.op186_6x(op)

	// ---- 60–7F：條件跳躍（8086 的 60–6F 是 70–7F 的別名）----------------
	case op >= 0x60 && op <= 0x7F:
		off := int16(int8(c.fetch8()))
		if c.cond(op & 0x0F) {
			c.IP = uint16(int16(c.IP) + off)
		}

	// ---- 80–83：ALU r/m, imm --------------------------------------------
	case op >= 0x80 && op <= 0x83:
		return c.aluImm(op)

	// ---- 84–8F ----------------------------------------------------------
	case op == 0x84: // TEST r/m8, r8
		m := c.decodeModRM()
		c.logic(uint16(c.get8(m.rm)&c.reg8(m.reg)), 8)
	case op == 0x85: // TEST r/m16, r16
		m := c.decodeModRM()
		c.logic(c.get16(m.rm)&c.R[m.reg], 16)
	case op == 0x86: // XCHG r/m8, r8
		m := c.decodeModRM()
		a, b := c.get8(m.rm), c.reg8(m.reg)
		c.set8(m.rm, b)
		c.setReg8(m.reg, a)
	case op == 0x87: // XCHG r/m16, r16
		m := c.decodeModRM()
		a, b := c.get16(m.rm), c.R[m.reg]
		c.set16(m.rm, b)
		c.R[m.reg] = a
	case op == 0x88: // MOV r/m8, r8
		m := c.decodeModRM()
		c.set8(m.rm, c.reg8(m.reg))
	case op == 0x89: // MOV r/m16, r16
		m := c.decodeModRM()
		c.set16(m.rm, c.R[m.reg])
	case op == 0x8A: // MOV r8, r/m8
		m := c.decodeModRM()
		c.setReg8(m.reg, c.get8(m.rm))
	case op == 0x8B: // MOV r16, r/m16
		m := c.decodeModRM()
		c.R[m.reg] = c.get16(m.rm)
	case op == 0x8C: // MOV r/m16, Sreg
		m := c.decodeModRM()
		c.set16(m.rm, c.Seg[m.reg&3])
	case op == 0x8D: // LEA r16, m
		m := c.decodeModRM()
		// LEA 取的是位移，不碰記憶體。r/m 是暫存器時 8086 的行為未定義；
		// 這裡回位移欄位 0，與實機一致（測資會遮）。
		c.R[m.reg] = m.rm.off
	case op == 0x8E: // MOV Sreg, r/m16
		m := c.decodeModRM()
		c.Seg[m.reg&3] = c.get16(m.rm)
	case op == 0x8F: // POP r/m16
		m := c.decodeModRM()
		c.set16(m.rm, c.pop())

	// ---- 90–9F ----------------------------------------------------------
	case op >= 0x90 && op <= 0x97: // XCHG AX, r16（90 ＝ NOP）
		r := int(op - 0x90)
		c.R[AX], c.R[r] = c.R[r], c.R[AX]
	case op == 0x98: // CBW
		c.R[AX] = uint16(int16(int8(uint8(c.R[AX]))))
	case op == 0x99: // CWD
		if c.R[AX]&0x8000 != 0 {
			c.R[DX] = 0xFFFF
		} else {
			c.R[DX] = 0
		}
	case op == 0x9A: // CALL far ptr16:16
		off := c.fetch16()
		seg := c.fetch16()
		c.push(c.Seg[CS])
		c.push(c.IP)
		c.Seg[CS], c.IP = seg, off
	case op == 0x9B: // WAIT
	case op == 0x9C: // PUSHF
		c.push(c.Flags)
	case op == 0x9D: // POPF
		c.SetFlags(c.pop())
	case op == 0x9E: // SAHF
		c.SetFlags(c.Flags&0xFF00 | uint16(c.reg8(4)))
	case op == 0x9F: // LAHF
		c.setReg8(4, uint8(c.Flags))

	// ---- A0–A3：MOV 與絕對位址 ------------------------------------------
	case op == 0xA0:
		c.setReg8(0, c.Bus.Read8(Addr(c.dataSeg(DS), c.fetch16())))
	case op == 0xA1:
		c.R[AX] = c.read16(c.dataSeg(DS), c.fetch16())
	case op == 0xA2:
		c.Bus.Write8(Addr(c.dataSeg(DS), c.fetch16()), uint8(c.R[AX]))
	case op == 0xA3:
		c.write16(c.dataSeg(DS), c.fetch16(), c.R[AX])

	// ---- A4–AF：字串指令 -------------------------------------------------
	case op >= 0xA4 && op <= 0xAF && op != 0xA8 && op != 0xA9:
		c.stringOp(op)

	case op == 0xA8: // TEST AL, imm8
		c.logic(uint16(uint8(c.R[AX])&c.fetch8()), 8)
	case op == 0xA9: // TEST AX, imm16
		c.logic(c.R[AX]&c.fetch16(), 16)

	// ---- B0–BF：MOV r, imm ----------------------------------------------
	case op >= 0xB0 && op <= 0xB7:
		c.setReg8(int(op-0xB0), c.fetch8())
	case op >= 0xB8 && op <= 0xBF:
		c.R[op-0xB8] = c.fetch16()

	// ---- C0–CF ----------------------------------------------------------
	//
	// ⚠ **C0／C1／C8／C9 也是機型相關的**（`docs/spec/002` §1.1）：
	// 8086 拿它們當 `RET`／`RETF` 的別名，80186 給了移位 imm8 與
	// `ENTER`／`LEAVE`。長度同樣不同，同樣不會報錯。
	case (op == 0xC0 || op == 0xC1) && c.Model >= Model80186:
		return c.shiftImm(op)
	case op == 0xC8 && c.Model >= Model80186:
		c.enter()
	case op == 0xC9 && c.Model >= Model80186:
		c.leave()

	case op == 0xC0 || op == 0xC2: // RET imm16（C0 是 8086 的別名）
		n := c.fetch16()
		c.IP = c.pop()
		c.R[SP] += n
	case op == 0xC1 || op == 0xC3: // RET
		c.IP = c.pop()
	case op == 0xC4: // LES r16, m
		m := c.decodeModRM()
		c.R[m.reg] = c.get16(m.rm)
		c.Seg[ES] = c.read16(m.rm.seg, m.rm.off+2)
	case op == 0xC5: // LDS r16, m
		m := c.decodeModRM()
		c.R[m.reg] = c.get16(m.rm)
		c.Seg[DS] = c.read16(m.rm.seg, m.rm.off+2)
	case op == 0xC6: // MOV r/m8, imm8
		m := c.decodeModRM()
		c.set8(m.rm, c.fetch8())
	case op == 0xC7: // MOV r/m16, imm16
		m := c.decodeModRM()
		c.set16(m.rm, c.fetch16())
	case op == 0xC8 || op == 0xCA: // RETF imm16（C8 是別名）
		n := c.fetch16()
		c.IP = c.pop()
		c.Seg[CS] = c.pop()
		c.R[SP] += n
	case op == 0xC9 || op == 0xCB: // RETF
		c.IP = c.pop()
		c.Seg[CS] = c.pop()
	case op == 0xCC: // INT 3
		c.doInt(3)
	case op == 0xCD: // INT imm8
		c.doInt(c.fetch8())
	case op == 0xCE: // INTO
		if c.Flag(OF) {
			c.doInt(4)
		}
	case op == 0xCF: // IRET
		c.IP = c.pop()
		c.Seg[CS] = c.pop()
		c.SetFlags(c.pop())

	// ---- D0–D3：移位與旋轉 ----------------------------------------------
	case op >= 0xD0 && op <= 0xD3:
		return c.shiftGroup(op)

	case op == 0xD4: // AAM imm8
		imm := c.fetch8()
		al := uint8(c.R[AX])
		if imm == 0 {
			// **除數 0 時 AX 完全不動**，但旗標照樣照「結果 0」設一次
			// 才進 INT 0——那個值會被推上堆疊，所以不是無關緊要的細節。
			// 實機資料：SingleStepTests D4 檔裡 47 筆 `aam 0h`，
			// 全部是 ZF=1 PF=1 SF=0 CF=0 AF=0 OF=0、DF 保留。
			c.logic(0, 8)
			c.divideError()
			return nil
		}
		c.setReg8(4, al/imm)
		c.setReg8(0, al%imm)
		// CF／AF／OF 在語料裡被遮掉（metadata 的 flags-mask ＝ F7EE），
		// 所以清掉是**我們挑的確定值**，不是實機保證。
		c.logic(uint16(al%imm), 8)
	case op == 0xD5: // AAD imm8
		imm := c.fetch8()
		al := uint8(uint16(c.reg8(4))*uint16(imm) + uint16(uint8(c.R[AX])))
		c.setReg8(0, al)
		c.setReg8(4, 0)
		c.logic(uint16(al), 8) // CF／AF／OF 同上，被遮掉
	case op == 0xD6: // SALC（未公開）
		if c.Flag(CF) {
			c.setReg8(0, 0xFF)
		} else {
			c.setReg8(0, 0x00)
		}
	case op == 0xD7: // XLAT
		c.setReg8(0, c.Bus.Read8(Addr(c.dataSeg(DS), c.R[BX]+uint16(uint8(c.R[AX])))))
	case op >= 0xD8 && op <= 0xDF: // ESC：沒有共處理器時只做記憶體讀取
		m := c.decodeModRM()
		if !m.rm.isReg {
			c.get16(m.rm)
		}

	// ---- E0–EF ----------------------------------------------------------
	case op >= 0xE0 && op <= 0xE3:
		off := int16(int8(c.fetch8()))
		take := false
		switch op {
		case 0xE0: // LOOPNZ
			c.R[CX]--
			take = c.R[CX] != 0 && !c.Flag(ZF)
		case 0xE1: // LOOPZ
			c.R[CX]--
			take = c.R[CX] != 0 && c.Flag(ZF)
		case 0xE2: // LOOP
			c.R[CX]--
			take = c.R[CX] != 0
		case 0xE3: // JCXZ
			take = c.R[CX] == 0
		}
		if take {
			c.IP = uint16(int16(c.IP) + off)
		}
	case op == 0xE4:
		c.setReg8(0, c.Bus.In8(uint16(c.fetch8())))
	case op == 0xE5:
		p := uint16(c.fetch8())
		c.R[AX] = uint16(c.Bus.In8(p)) | uint16(c.Bus.In8(p+1))<<8
	case op == 0xE6:
		c.Bus.Out8(uint16(c.fetch8()), uint8(c.R[AX]))
	case op == 0xE7:
		p := uint16(c.fetch8())
		c.Bus.Out8(p, uint8(c.R[AX]))
		c.Bus.Out8(p+1, uint8(c.R[AX]>>8))
	case op == 0xE8: // CALL rel16
		off := int16(c.fetch16())
		c.push(c.IP)
		c.IP = uint16(int16(c.IP) + off)
	case op == 0xE9: // JMP rel16
		off := int16(c.fetch16())
		c.IP = uint16(int16(c.IP) + off)
	case op == 0xEA: // JMP far
		off := c.fetch16()
		seg := c.fetch16()
		c.Seg[CS], c.IP = seg, off
	case op == 0xEB: // JMP rel8
		off := int16(int8(c.fetch8()))
		c.IP = uint16(int16(c.IP) + off)
	case op == 0xEC:
		c.setReg8(0, c.Bus.In8(c.R[DX]))
	case op == 0xED:
		c.R[AX] = uint16(c.Bus.In8(c.R[DX])) | uint16(c.Bus.In8(c.R[DX]+1))<<8
	case op == 0xEE:
		c.Bus.Out8(c.R[DX], uint8(c.R[AX]))
	case op == 0xEF:
		c.Bus.Out8(c.R[DX], uint8(c.R[AX]))
		c.Bus.Out8(c.R[DX]+1, uint8(c.R[AX]>>8))

	// ---- F4–FF ----------------------------------------------------------
	case op == 0xF4: // HLT
		c.Halted = true
	case op == 0xF5: // CMC
		c.setFlag(CF, !c.Flag(CF))
	case op == 0xF6 || op == 0xF7:
		return c.group3(op)
	case op == 0xF8:
		c.setFlag(CF, false)
	case op == 0xF9:
		c.setFlag(CF, true)
	case op == 0xFA:
		c.setFlag(IF, false)
	case op == 0xFB:
		c.setFlag(IF, true)
	case op == 0xFC:
		c.setFlag(DF, false)
	case op == 0xFD:
		c.setFlag(DF, true)
	case op == 0xFE || op == 0xFF:
		return c.group45(op)

	default:
		return c.errf(op, "未實作的 opcode")
	}
	return nil
}

// cond 是條件跳躍的判定，索引就是 opcode 的低 4 位元。
func (c *CPU) cond(n uint8) bool {
	switch n {
	case 0x0: // JO
		return c.Flag(OF)
	case 0x1: // JNO
		return !c.Flag(OF)
	case 0x2: // JB／JC
		return c.Flag(CF)
	case 0x3: // JNB
		return !c.Flag(CF)
	case 0x4: // JZ
		return c.Flag(ZF)
	case 0x5: // JNZ
		return !c.Flag(ZF)
	case 0x6: // JBE
		return c.Flag(CF) || c.Flag(ZF)
	case 0x7: // JA
		return !c.Flag(CF) && !c.Flag(ZF)
	case 0x8: // JS
		return c.Flag(SF)
	case 0x9: // JNS
		return !c.Flag(SF)
	case 0xA: // JP
		return c.Flag(PF)
	case 0xB: // JNP
		return !c.Flag(PF)
	case 0xC: // JL
		return c.Flag(SF) != c.Flag(OF)
	case 0xD: // JGE
		return c.Flag(SF) == c.Flag(OF)
	case 0xE: // JLE
		return c.Flag(ZF) || c.Flag(SF) != c.Flag(OF)
	default: // JG
		return !c.Flag(ZF) && c.Flag(SF) == c.Flag(OF)
	}
}

// aluGroup 跑 00–3D 那八個運算。低 3 位元選定址：
//
//	0: r/m8, r8    1: r/m16, r16   2: r8, r/m8   3: r16, r/m16
//	4: AL, imm8    5: AX, imm16
func (c *CPU) aluGroup(op uint8) error {
	kind := int(op >> 3) // 0=ADD 1=OR 2=ADC 3=SBB 4=AND 5=SUB 6=XOR 7=CMP
	form := op & 7
	switch form {
	case 0, 1, 2, 3:
		m := c.decodeModRM()
		wide := form&1 == 1
		dst, src := m.rm, regOperand(m.reg)
		if form >= 2 {
			dst, src = src, dst
		}
		if wide {
			r, store := c.alu16(kind, c.get16(dst), c.get16(src))
			if store {
				c.set16(dst, r)
			}
		} else {
			r, store := c.alu8(kind, c.get8(dst), c.get8(src))
			if store {
				c.set8(dst, r)
			}
		}
	case 4:
		r, store := c.alu8(kind, uint8(c.R[AX]), c.fetch8())
		if store {
			c.setReg8(0, r)
		}
	case 5:
		r, store := c.alu16(kind, c.R[AX], c.fetch16())
		if store {
			c.R[AX] = r
		}
	}
	return nil
}

// alu8／alu16 回傳 (結果, 要不要寫回去)。CMP 只設旗標，不寫回。
func (c *CPU) alu8(kind int, a, b uint8) (uint8, bool) {
	r, store := c.aluCore(kind, uint32(a), uint32(b), 8)
	return uint8(r), store
}

func (c *CPU) alu16(kind int, a, b uint16) (uint16, bool) {
	return c.aluCore(kind, uint32(a), uint32(b), 16)
}

func (c *CPU) aluCore(kind int, a, b uint32, size int) (uint16, bool) {
	carry := uint32(0)
	if c.Flag(CF) {
		carry = 1
	}
	switch kind {
	case 0:
		return c.add(a, b, 0, size), true
	case 1:
		return c.logic(uint16(a|b), size), true
	case 2:
		return c.add(a, b, carry, size), true
	case 3:
		return c.sub(a, b, carry, size), true
	case 4:
		return c.logic(uint16(a&b), size), true
	case 5:
		return c.sub(a, b, 0, size), true
	case 6:
		return c.logic(uint16(a^b), size), true
	default: // CMP
		c.sub(a, b, 0, size)
		return 0, false
	}
}

// aluImm 是 80–83：r/m 與立即數。
//
//	80: r/m8,  imm8      81: r/m16, imm16
//	82: r/m8,  imm8（**80 的別名**）
//	83: r/m16, imm8（**有號延伸**成 16 位元）
func (c *CPU) aluImm(op uint8) error {
	m := c.decodeModRM()
	kind := m.reg
	switch op {
	case 0x80, 0x82:
		imm := c.fetch8()
		r, store := c.alu8(kind, c.get8(m.rm), imm)
		if store {
			c.set8(m.rm, r)
		}
	case 0x81:
		imm := c.fetch16()
		r, store := c.alu16(kind, c.get16(m.rm), imm)
		if store {
			c.set16(m.rm, r)
		}
	case 0x83:
		imm := uint16(int16(int8(c.fetch8())))
		r, store := c.alu16(kind, c.get16(m.rm), imm)
		if store {
			c.set16(m.rm, r)
		}
	}
	return nil
}

// shiftGroup 是 D0–D3。
//
//	D0: r/m8,  1     D1: r/m16, 1
//	D2: r/m8,  CL    D3: r/m16, CL
//
// **CL 不遮罩成 5 位元**，而且 **CL ＝ 0 時完全不動旗標**
// （`docs/spec/002` §4 第 2 點）。
func (c *CPU) shiftGroup(op uint8) error {
	m := c.decodeModRM()
	count := uint8(1)
	if op >= 0xD2 {
		count = uint8(c.R[CX])
	}
	if op&1 == 0 {
		c.set8(m.rm, uint8(c.shiftRotate(m.reg, uint16(c.get8(m.rm)), count, 8)))
	} else {
		c.set16(m.rm, c.shiftRotate(m.reg, c.get16(m.rm), count, 16))
	}
	return nil
}

// group3 是 F6／F7：TEST／NOT／NEG／MUL／IMUL／DIV／IDIV。
// `/1` 是 `/0`（TEST）的別名。
func (c *CPU) group3(op uint8) error {
	m := c.decodeModRM()
	wide := op == 0xF7
	switch m.reg {
	case 0, 1: // TEST r/m, imm
		if wide {
			c.logic(c.get16(m.rm)&c.fetch16(), 16)
		} else {
			c.logic(uint16(c.get8(m.rm)&c.fetch8()), 8)
		}
	case 2: // NOT（**不動任何旗標**）
		if wide {
			c.set16(m.rm, ^c.get16(m.rm))
		} else {
			c.set8(m.rm, ^c.get8(m.rm))
		}
	case 3: // NEG
		if wide {
			c.set16(m.rm, c.neg(c.get16(m.rm), 16))
		} else {
			c.set8(m.rm, uint8(c.neg(uint16(c.get8(m.rm)), 8)))
		}
	case 4: // MUL
		if wide {
			c.mul16(c.get16(m.rm))
		} else {
			c.mul8(c.get8(m.rm))
		}
	case 5: // IMUL
		if wide {
			c.imul16(c.get16(m.rm))
		} else {
			c.imul8(c.get8(m.rm))
		}
	case 6: // DIV
		ok := false
		if wide {
			ok = c.div16(c.get16(m.rm))
		} else {
			ok = c.div8(c.get8(m.rm))
		}
		if !ok {
			c.divideError()
		}
	case 7: // IDIV
		ok := false
		if wide {
			ok = c.idiv16(c.get16(m.rm))
		} else {
			ok = c.idiv8(c.get8(m.rm))
		}
		if !ok {
			c.divideError()
		}
	}
	return nil
}

// group45 是 FE／FF。FE 只有 /0 INC 與 /1 DEC；FF 多了呼叫、跳躍與 PUSH。
// FF /7 是 /6（PUSH）的別名。
func (c *CPU) group45(op uint8) error {
	m := c.decodeModRM()
	if op == 0xFE {
		switch m.reg {
		case 0:
			c.set8(m.rm, uint8(c.inc(uint16(c.get8(m.rm)), 8)))
		case 1:
			c.set8(m.rm, uint8(c.dec(uint16(c.get8(m.rm)), 8)))
		default:
			// FE 的 /2–/7 在 8086 上會被當成 /0 或 /1 的別名執行。
			// 實機行為未定義；照 SingleStepTests 的 metadata 是 undefined，
			// 這裡走 INC，讓行為至少是確定的。
			c.set8(m.rm, uint8(c.inc(uint16(c.get8(m.rm)), 8)))
		}
		return nil
	}
	switch m.reg {
	case 0:
		c.set16(m.rm, c.inc(c.get16(m.rm), 16))
	case 1:
		c.set16(m.rm, c.dec(c.get16(m.rm), 16))
	case 2: // CALL near r/m16
		t := c.get16(m.rm)
		c.push(c.IP)
		c.IP = t
	case 3: // CALL far m16:16
		off := c.get16(m.rm)
		seg := c.read16(m.rm.seg, m.rm.off+2)
		c.push(c.Seg[CS])
		c.push(c.IP)
		c.Seg[CS], c.IP = seg, off
	case 4: // JMP near r/m16
		c.IP = c.get16(m.rm)
	case 5: // JMP far m16:16
		off := c.get16(m.rm)
		seg := c.read16(m.rm.seg, m.rm.off+2)
		c.Seg[CS], c.IP = seg, off
	case 6, 7: // PUSH r/m16
		c.pushOperand(m.rm)
	}
	return nil
}

// pushOperand 推一個 r/m16 運算元。
//
// ⚠ **運算元是 `SP` 時要先減再讀**：8086 的 `PUSH SP` 推的是**已經減 2
// 之後**的值，286 以上推的才是舊值（`docs/spec/002` §4 第 1 點）。
//
// 不能寫成 `c.push(c.get16(o))`——Go 會**先算好引數**再呼叫，
// 於是推進去的是舊 SP，剛好變成 286 的行為。這個錯編譯得過、vet 過，
// 其他七個暫存器全對，只有 `PUSH SP` 一道指令差 2。
//
// **同一個坑有兩條路徑**：`50`–`57`（`PUSH r16`）與 `FF /6`（`PUSH r/m16`）。
// 修好第一條之後 `FF.6`／`FF.7` 還是紅的——所以這裡收成一支，
// 不讓第三條路徑再踩一次。
func (c *CPU) pushOperand(o operand) {
	if o.isReg && o.reg == SP {
		c.R[SP] -= 2
		c.write16(c.Seg[SS], c.R[SP], c.R[SP])
		return
	}
	c.push(c.get16(o))
}

// doInt 走 IntHook；沒攔就跳真正的向量表。
//
// **沒實作的服務不會安靜地變成 nop**——hook 回 false 就照跳，
// 跳到沒填的向量會很快出事，那正是我們要的訊號（`rich2/docs/re/005` §3.1）。
func (c *CPU) doInt(n uint8) {
	if c.IntHook != nil && c.IntHook(c, n) {
		return
	}
	c.Interrupt(n)
}

// divideError 是 INT 0。
//
// **8086 推的返回位址指向下一道指令**，不是指令本身（`docs/spec/002` §4 第 3 點）
// ——此時 IP 已經走完整道指令，所以直接走 Interrupt 就是對的。
func (c *CPU) divideError() { c.Interrupt(0) }
