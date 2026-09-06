package cpu

// operand 是一個 ModRM 解出來的目的地／來源：不是暫存器就是記憶體。
//
// 拆成一個值而不是兩支函式，是為了讓「讀出來、算一算、寫回去」那一類指令
// （`ADD r/m, r`、`INC r/m`…）只解一次位址。**8086 的 read-modify-write
// 就是同一個位址讀一次寫一次**，解兩次在有段前綴時會漂。
type operand struct {
	isReg bool
	reg   int    // isReg 時的暫存器索引
	seg   uint16 // 記憶體時的段值（已經套過段前綴）
	off   uint16 // 記憶體時的位移
}

// modrm 是解出來的 ModRM 位元組：reg 欄位與 r/m 運算元。
type modrm struct {
	reg int
	rm  operand
	mod uint8
}

// defaultSeg 是每一種 r/m 定址預設走哪個段。
//
// **只有用到 BP 的那幾種預設是 SS**（[BP+SI]、[BP+DI]、[BP]、[BP+disp]），
// 其餘都是 DS。這一條寫錯不會報錯——它會讓堆疊上的區域變數讀到資料段的內容，
// 而那看起來像「資料解錯了」。
func (c *CPU) decodeModRM() modrm {
	b := c.fetch8()
	mod := b >> 6
	reg := int(b>>3) & 7
	rm := int(b) & 7

	if mod == 3 {
		return modrm{reg: reg, mod: mod, rm: operand{isReg: true, reg: rm}}
	}

	var off uint16
	def := DS
	switch rm {
	case 0:
		off = c.R[BX] + c.R[SI]
	case 1:
		off = c.R[BX] + c.R[DI]
	case 2:
		off = c.R[BP] + c.R[SI]
		def = SS
	case 3:
		off = c.R[BP] + c.R[DI]
		def = SS
	case 4:
		off = c.R[SI]
	case 5:
		off = c.R[DI]
	case 6:
		if mod == 0 {
			// mod=0 rm=6 是唯一的例外：不是 [BP]，是 16 位元絕對位址，
			// 而且**段回到 DS**。
			off = c.fetch16()
		} else {
			off = c.R[BP]
			def = SS
		}
	case 7:
		off = c.R[BX]
	}

	switch mod {
	case 1:
		// 8 位元位移是**有號**的。當成無號加會讓 [BP-2] 變成 [BP+254]。
		off += uint16(int16(int8(c.fetch8())))
	case 2:
		off += c.fetch16()
	}

	return modrm{reg: reg, mod: mod, rm: operand{seg: c.dataSeg(def), off: off}}
}

func (c *CPU) get8(o operand) uint8 {
	if o.isReg {
		return c.reg8(o.reg)
	}
	return c.Bus.Read8(Addr(o.seg, o.off))
}

func (c *CPU) set8(o operand, v uint8) {
	if o.isReg {
		c.setReg8(o.reg, v)
		return
	}
	c.Bus.Write8(Addr(o.seg, o.off), v)
}

func (c *CPU) get16(o operand) uint16 {
	if o.isReg {
		return c.R[o.reg]
	}
	return c.read16(o.seg, o.off)
}

func (c *CPU) set16(o operand, v uint16) {
	if o.isReg {
		c.R[o.reg] = v
		return
	}
	c.write16(o.seg, o.off, v)
}

// regOperand 把 ModRM 的 reg 欄位包成 operand，讓 r/m 與 r 兩邊走同一組存取函式。
func regOperand(i int) operand { return operand{isReg: true, reg: i} }
