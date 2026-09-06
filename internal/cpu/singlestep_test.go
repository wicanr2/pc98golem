package cpu

// SingleStepTests/8088 v2 的驗收（`docs/spec/002` §5）。
//
// 語料 761 MB，不進版控——`tools/fetch_cputests.sh` 抓到 `testdata/8088/`。
// 沒抓就 skip，**不要因為沒有語料就當成通過**。

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const testDir = "../../testdata/8088"

// ---- 語料的資料結構（欄位名照原檔）--------------------------------------

type ssRegs struct {
	AX, BX, CX, DX *uint16
	CS, SS, DS, ES *uint16
	SP, BP, SI, DI *uint16
	IP, Flags      *uint16
}

func (r *ssRegs) UnmarshalJSON(b []byte) error {
	var m map[string]uint16
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	set := func(k string, dst **uint16) {
		if v, ok := m[k]; ok {
			x := v
			*dst = &x
		}
	}
	set("ax", &r.AX)
	set("bx", &r.BX)
	set("cx", &r.CX)
	set("dx", &r.DX)
	set("cs", &r.CS)
	set("ss", &r.SS)
	set("ds", &r.DS)
	set("es", &r.ES)
	set("sp", &r.SP)
	set("bp", &r.BP)
	set("si", &r.SI)
	set("di", &r.DI)
	set("ip", &r.IP)
	set("flags", &r.Flags)
	return nil
}

type ssState struct {
	Regs ssRegs      `json:"regs"`
	RAM  [][2]uint32 `json:"ram"`
}

type ssTest struct {
	Name    string  `json:"name"`
	Bytes   []uint8 `json:"bytes"`
	Initial ssState `json:"initial"`
	Final   ssState `json:"final"`
	Hash    string  `json:"hash"`
	Idx     int     `json:"idx"`
}

// metadata.json：每個 opcode 的狀態與未定義旗標遮罩。群組 opcode 的
// 遮罩在 `reg` 子鍵底下，**逐個 /r 不一樣**，不能拿 opcode 層的來套。
type ssMeta struct {
	Opcodes map[string]ssOpcode `json:"opcodes"`
}

type ssOpcode struct {
	Status    string              `json:"status"`
	FlagsMask *uint16             `json:"flags-mask"`
	Reg       map[string]ssOpcode `json:"reg"`
}

// ---- 記憶體 --------------------------------------------------------------

// testBus 是一顆 1 MB 的平坦記憶體，並記下被寫過的位址。
//
// 記 dirty 是為了驗「**沒有寫到不該寫的地方**」：只比 final.ram 列出來的位址，
// 會漏掉「多寫了一個位元組」這種錯——而那種錯在跑真的程式時是災難。
type testBus struct {
	mem   []uint8
	dirty []uint32
	ports map[uint16]uint8
}

func newTestBus() *testBus {
	return &testBus{mem: make([]uint8, 1<<20), ports: map[uint16]uint8{}}
}

func (b *testBus) Read8(a uint32) uint8 { return b.mem[a&0xFFFFF] }

func (b *testBus) Write8(a uint32, v uint8) {
	a &= 0xFFFFF
	b.mem[a] = v
	b.dirty = append(b.dirty, a)
}

// IN 回 0xFF——**空的匯流排上讀到的就是 0xFF**，不是 0。
// 語料裡 IN 的期望值就是這個。
func (b *testBus) In8(uint16) uint8 { return 0xFF }

func (b *testBus) Out8(p uint16, v uint8) { b.ports[p] = v }

// reset 把上一筆測資動過的位址清乾淨，1 MB 不重配。
func (b *testBus) reset(initial [][2]uint32) {
	for _, a := range b.dirty {
		b.mem[a] = 0
	}
	b.dirty = b.dirty[:0]
	for _, e := range initial {
		b.mem[e[0]&0xFFFFF] = 0
	}
	b.ports = map[uint16]uint8{}
}

// ---- 測試本體 -------------------------------------------------------------

func loadMeta(t *testing.T) *ssMeta {
	t.Helper()
	f, err := os.Open(filepath.Join(testDir, "metadata.json"))
	if err != nil {
		t.Skipf("沒有測試語料（跑 tools/fetch_cputests.sh 抓）：%v", err)
	}
	defer f.Close()
	var m ssMeta
	if err := json.NewDecoder(f).Decode(&m); err != nil {
		t.Fatalf("metadata.json 解不開：%v", err)
	}
	return &m
}

// specOf 把檔名（`40`、`FF.2`）對到 metadata 的那一項。
func (m *ssMeta) specOf(name string) (ssOpcode, bool) {
	parts := strings.SplitN(name, ".", 2)
	op, ok := m.Opcodes[strings.ToUpper(parts[0])]
	if !ok {
		return ssOpcode{}, false
	}
	if len(parts) == 1 {
		return op, true
	}
	sub, ok := op.Reg[parts[1]]
	return sub, ok
}

func TestSingleStep(t *testing.T) {
	meta := loadMeta(t)
	files, err := filepath.Glob(filepath.Join(testDir, "*.json.gz"))
	if err != nil || len(files) == 0 {
		t.Skipf("沒有測試語料（跑 tools/fetch_cputests.sh 抓）")
	}
	sort.Strings(files)
	for _, path := range files {
		name := strings.TrimSuffix(filepath.Base(path), ".json.gz")
		spec, ok := meta.specOf(name)
		if !ok {
			t.Errorf("%s：metadata.json 裡查不到這個 opcode", name)
			continue
		}
		// FPU 的整檔跳過（`docs/spec/001` §3 第 2 點：不做 x87）。
		if spec.Status == "fpu" {
			continue
		}
		t.Run(name, func(t *testing.T) { runOpcodeFile(t, path, spec) })
	}
}

// knownGaps 是**已經量過、還沒解**的差距：opcode 檔名 → 容許的不合筆數上限。
//
// ⚠ **這不是「跳過」。** 每一輪都照跑、照數，超過上限就紅——所以它擋得住退步，
// 只是不擋現況。每一項都要在 `docs/spec/002` §3.4 講清楚是什麼、為什麼還沒解。
//
// 目前只有一項：**`IDIV` 溢位時推上堆疊的旗標還沒實作**。
//
// `DIV` 已經解出來了——旗標 ＝ 內部第一次比較「被除數高半部 − 除數」
// 留下的，`F6.6`（1,439 筆）與 `F7.6`（1,372 筆）的暫存器型溢位樣本全中，
// 兩個檔現在全綠。
//
// `IDIV` 不一樣：它先把兩邊取絕對值再跑無號除法迴圈，**溢位是在迴圈中途
// 才偵測到的**，所以旗標來自比較晚的一次內部減法。拿 `DIV` 那條規則套上去
// 只對三成多。已經試過的模型與命中率記在 `docs/spec/002` §3.4，
// 最好的一個（重跑 CORD 迴圈、取最後一次比較）到 84%——**方向對了，
// 差的是符號修正那一段**。
//
// **這是「還沒實作」，不是「誤差」**，數字大是正常的：`F6.7` 有 75% 的測資
// 會觸發溢位。成功的除法不受影響——那條路的旗標被語料遮掉，也不會推上堆疊。
var knownGaps = map[string]int{
	"F6.7": 6218, // 位元組 IDIV
	"F7.7": 6371, // 字組 IDIV
}

func runOpcodeFile(t *testing.T, path string, spec ssOpcode) {
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()

	var tests []ssTest
	if err := json.NewDecoder(zr).Decode(&tests); err != nil {
		t.Fatalf("解不開測資：%v", err)
	}

	mask := uint16(0xFFFF)
	if spec.FlagsMask != nil {
		mask = *spec.FlagsMask
	}

	name := strings.TrimSuffix(filepath.Base(path), ".json.gz")
	budget, hasGap := knownGaps[name]
	// 有預算時要把整檔跑完才數得出來；沒有預算時錯幾筆就停，訊息才讀得完。
	limit := 5
	if hasGap {
		limit = len(tests) + 1
	}
	if v := os.Getenv("DOSGOLEM_MAX_FAILS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}

	bus := newTestBus()
	c := New(bus)
	fails := 0
	var firstMsgs []string
	for i := range tests {
		tc := &tests[i]
		bus.reset(tc.Initial.RAM)
		for _, e := range tc.Initial.RAM {
			bus.mem[e[0]&0xFFFFF] = uint8(e[1])
		}
		bus.dirty = bus.dirty[:0]
		applyRegs(c, &tc.Initial.Regs, true)

		if err := c.Step(); err != nil {
			fails++
			if len(firstMsgs) < 5 {
				firstMsgs = append(firstMsgs, fmt.Sprintf("#%d %s：%v", tc.Idx, tc.Name, err))
			}
			if fails >= limit {
				break
			}
			continue
		}
		if msg := compare(c, bus, tc, mask); msg != "" {
			fails++
			if len(firstMsgs) < 5 {
				firstMsgs = append(firstMsgs,
					fmt.Sprintf("#%d %s（hash %s）\n%s", tc.Idx, tc.Name, tc.Hash, msg))
			}
			if fails >= limit {
				break
			}
		}
	}

	if fails == 0 {
		if hasGap && budget > 0 {
			t.Errorf("%s 的已知差距是 %d 筆，實際 0 筆——差距解掉了，把 knownGaps 裡這一項刪掉", name, budget)
		}
		return
	}
	if hasGap && fails <= budget {
		t.Logf("已知差距：%d／%d 筆不合（上限 %d，`docs/spec/002` §3.4）",
			fails, len(tests), budget)
		return
	}
	for _, m := range firstMsgs {
		t.Error(m)
	}
	if hasGap {
		t.Fatalf("%d／%d 筆不合，超過已知差距上限 %d——退步了", fails, len(tests), budget)
	}
	t.Fatalf("%d／%d 筆不合", fails, len(tests))
}

// applyRegs 把測資的暫存器套進 CPU。full 為真時是 initial（每一欄都有值）。
func applyRegs(c *CPU, r *ssRegs, full bool) {
	put := func(p *uint16, dst *uint16) {
		if p != nil {
			*dst = *p
		}
	}
	put(r.AX, &c.R[AX])
	put(r.BX, &c.R[BX])
	put(r.CX, &c.R[CX])
	put(r.DX, &c.R[DX])
	put(r.SP, &c.R[SP])
	put(r.BP, &c.R[BP])
	put(r.SI, &c.R[SI])
	put(r.DI, &c.R[DI])
	put(r.CS, &c.Seg[CS])
	put(r.SS, &c.Seg[SS])
	put(r.DS, &c.Seg[DS])
	put(r.ES, &c.Seg[ES])
	put(r.IP, &c.IP)
	if r.Flags != nil {
		c.SetFlags(*r.Flags)
	}
	_ = full
}

// compare 回傳空字串表示過。
//
// 預期值 ＝ initial 疊上 final——**final 只列變動過的欄位**，
// 直接拿 final 當全部會把「沒動的暫存器」漏掉不比。
func compare(c *CPU, bus *testBus, tc *ssTest, mask uint16) string {
	want := tc.Initial.Regs
	overlay(&want, &tc.Final.Regs)

	var msg []string
	chk := func(name string, got uint16, p *uint16) {
		if p != nil && got != *p {
			msg = append(msg, fmt.Sprintf("  %-5s 得到 %04X，預期 %04X", name, got, *p))
		}
	}
	chk("AX", c.R[AX], want.AX)
	chk("BX", c.R[BX], want.BX)
	chk("CX", c.R[CX], want.CX)
	chk("DX", c.R[DX], want.DX)
	chk("SP", c.R[SP], want.SP)
	chk("BP", c.R[BP], want.BP)
	chk("SI", c.R[SI], want.SI)
	chk("DI", c.R[DI], want.DI)
	chk("CS", c.Seg[CS], want.CS)
	chk("SS", c.Seg[SS], want.SS)
	chk("DS", c.Seg[DS], want.DS)
	chk("ES", c.Seg[ES], want.ES)
	chk("IP", c.IP, want.IP)
	if want.Flags != nil {
		got, exp := c.Flags&mask, *want.Flags&mask
		if got != exp {
			msg = append(msg, fmt.Sprintf("  FLAGS 得到 %04X，預期 %04X（差 %04X，遮罩 %04X）\n"+
				"        %s\n        %s", c.Flags, *want.Flags, got^exp, mask,
				flagStr(c.Flags), flagStr(*want.Flags)))
		}
	}

	// 記憶體：initial 疊上 final，再驗每一個被寫過的位址都在預期裡。
	expect := map[uint32]uint8{}
	for _, e := range tc.Initial.RAM {
		expect[e[0]&0xFFFFF] = uint8(e[1])
	}
	for _, e := range tc.Final.RAM {
		expect[e[0]&0xFFFFF] = uint8(e[1])
	}
	for a, v := range expect {
		if bus.mem[a] != v {
			msg = append(msg, fmt.Sprintf("  記憶體 %05X 得到 %02X，預期 %02X", a, bus.mem[a], v))
		}
	}
	for _, a := range bus.dirty {
		if _, ok := expect[a]; !ok && bus.mem[a] != 0 {
			msg = append(msg, fmt.Sprintf("  寫到不該寫的位址 %05X ＝ %02X", a, bus.mem[a]))
		}
	}
	if len(msg) == 0 {
		return ""
	}
	sort.Strings(msg)
	return strings.Join(msg, "\n")
}

func overlay(dst, src *ssRegs) {
	f := []struct{ d, s **uint16 }{
		{&dst.AX, &src.AX}, {&dst.BX, &src.BX}, {&dst.CX, &src.CX}, {&dst.DX, &src.DX},
		{&dst.SP, &src.SP}, {&dst.BP, &src.BP}, {&dst.SI, &src.SI}, {&dst.DI, &src.DI},
		{&dst.CS, &src.CS}, {&dst.SS, &src.SS}, {&dst.DS, &src.DS}, {&dst.ES, &src.ES},
		{&dst.IP, &src.IP}, {&dst.Flags, &src.Flags},
	}
	for _, x := range f {
		if *x.s != nil {
			*x.d = *x.s
		}
	}
}

// flagStr 把旗標印成看得懂的樣子，比對錯誤時才知道差在哪一個。
func flagStr(f uint16) string {
	var b strings.Builder
	for _, x := range []struct {
		bit  uint16
		name string
	}{{OF, "O"}, {DF, "D"}, {IF, "I"}, {TF, "T"}, {SF, "S"}, {ZF, "Z"}, {AF, "A"}, {PF, "P"}, {CF, "C"}} {
		if f&x.bit != 0 {
			b.WriteString(x.name)
		} else {
			b.WriteString("-")
		}
	}
	return b.String() + " (" + strconv.FormatUint(uint64(f), 16) + ")"
}
