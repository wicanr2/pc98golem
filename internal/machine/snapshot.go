package machine

import "github.com/wicanr2/pc98golem/internal/cpu"

// Snapshot 是一份完整的機器狀態。
//
// ⚠ **「完整」是字面意思，包含所有內部時鐘。** 只存記憶體與 CPU 的話，
// 還原之後 `nextIRQ0` 還停在未來的某個大值——**計時器中斷從此不再送**，
// 於是遊戲的動畫停住、輪詢變少，而畫面看起來完全正常。
//
// 實測症狀：從快照展開四個變體，第一個（沒還原過）跑得好好的，
// 後面三個「點下去遊戲沒輪詢到」。查到最後是這裡漏了三個欄位。
type Snapshot struct {
	mem   []uint8
	regs  [8]uint16
	segs  [4]uint16
	ip    uint16
	flags uint16

	steps     uint64
	ticks     uint64
	portTicks uint64
	nextIRQ0  uint64
	pending   bool

	ports   map[uint16]uint8
	portsIn map[uint16]uint64

	dac      [256 * 3]uint8
	dacIndex uint8
	dacPhase uint8
}

// Mem 回快照裡的記憶體，給差分比對用。**不要改它。**
func (s *Snapshot) Mem() []uint8 { return s.mem }

// Snapshot 拍一份快照。1 MB 記憶體，約 1 毫秒。
func (m *Machine) Snapshot() *Snapshot {
	s := &Snapshot{
		mem:       make([]uint8, len(m.Mem)),
		regs:      m.CPU.R,
		segs:      m.CPU.Seg,
		ip:        m.CPU.IP,
		flags:     m.CPU.Flags,
		steps:     m.Steps,
		ticks:     m.Ticks,
		portTicks: m.portTicks,
		nextIRQ0:  m.nextIRQ0,
		pending:   m.irq0Pending,
		ports:     map[uint16]uint8{},
		portsIn:   map[uint16]uint64{},
		dac:       m.DAC,
		dacIndex:  m.dacIndex,
		dacPhase:  m.dacPhase,
	}
	copy(s.mem, m.Mem)
	for k, v := range m.Ports {
		s.ports[k] = v
	}
	for k, v := range m.PortsIn {
		s.portsIn[k] = v
	}
	return s
}

// Restore 把機器倒回快照。
func (m *Machine) Restore(s *Snapshot) {
	copy(m.Mem, s.mem)
	m.CPU.R, m.CPU.Seg, m.CPU.IP = s.regs, s.segs, s.ip
	m.CPU.SetFlags(s.flags)
	m.CPU.Halted = false

	m.Steps, m.Ticks = s.steps, s.ticks
	m.portTicks, m.nextIRQ0, m.irq0Pending = s.portTicks, s.nextIRQ0, s.pending

	m.Ports = map[uint16]uint8{}
	for k, v := range s.ports {
		m.Ports[k] = v
	}
	m.PortsIn = map[uint16]uint64{}
	for k, v := range s.portsIn {
		m.PortsIn[k] = v
	}
	m.PortLog = m.PortLog[:0]

	m.DAC, m.dacIndex, m.dacPhase = s.dac, s.dacIndex, s.dacPhase
}

// 讓 cpu 這個 import 有用途（Snapshot 裡的暫存器型別來自它）。
var _ = cpu.AX
