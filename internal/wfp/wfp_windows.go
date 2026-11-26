package wfp

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The fwpuclnt.dll calls used (lazy procs, like internal/iphlp: no new
// module, docs/decisions.md phase 21).
var (
	modFwpuclnt           = windows.NewLazySystemDLL("fwpuclnt.dll")
	procEngineOpen        = modFwpuclnt.NewProc("FwpmEngineOpen0")
	procEngineClose       = modFwpuclnt.NewProc("FwpmEngineClose0")
	procTransactionBegin  = modFwpuclnt.NewProc("FwpmTransactionBegin0")
	procTransactionCommit = modFwpuclnt.NewProc("FwpmTransactionCommit0")
	procTransactionAbort  = modFwpuclnt.NewProc("FwpmTransactionAbort0")
	procSubLayerAdd       = modFwpuclnt.NewProc("FwpmSubLayerAdd0")
	procSubLayerGetByKey  = modFwpuclnt.NewProc("FwpmSubLayerGetByKey0")
	procFilterAdd         = modFwpuclnt.NewProc("FwpmFilterAdd0")
	procGetAppIDFromFile  = modFwpuclnt.NewProc("FwpmGetAppIdFromFileName0")
	procFreeMemory        = modFwpuclnt.NewProc("FwpmFreeMemory0")
)

// SubLayer is the CrossStream sublayer key; every filter of the hub lives
// in it ("netsh wfp show filters" lists them under this key).
var SubLayer = windows.GUID{Data1: 0x5853f00d, Data2: 0x2121, Data3: 0x4c53, Data4: [8]byte{0x8a, 0x1e, 0x43, 0x72, 0x6f, 0x73, 0x73, 0x53}}

// ErrSubLayerNotFound is FWP_E_SUBLAYER_NOT_FOUND.
var ErrSubLayerNotFound = windows.Errno(0x80320007)

var (
	layerConnectV4 = windows.GUID{Data1: 0xc38d57d1, Data2: 0x05a7, Data3: 0x4c33, Data4: [8]byte{0x90, 0x4f, 0x7f, 0xbc, 0xee, 0xe6, 0x0e, 0x82}}
	layerConnectV6 = windows.GUID{Data1: 0x4a72393b, Data2: 0x319f, Data3: 0x44bc, Data4: [8]byte{0x84, 0xc3, 0xba, 0x54, 0xdc, 0xb3, 0xb6, 0xb4}}

	condAppID      = windows.GUID{Data1: 0xd78e1e87, Data2: 0x8644, Data3: 0x4ea5, Data4: [8]byte{0x94, 0x37, 0xd8, 0x09, 0xec, 0xef, 0xc9, 0x71}}
	condFlags      = windows.GUID{Data1: 0x632ce23b, Data2: 0x5167, Data3: 0x435c, Data4: [8]byte{0x86, 0xd7, 0xe9, 0x03, 0x68, 0x4a, 0xa8, 0x0c}}
	condLocalIface = windows.GUID{Data1: 0x4cd62a49, Data2: 0x59c3, Data3: 0x4969, Data4: [8]byte{0xb7, 0xf3, 0xbd, 0xa5, 0xd3, 0x28, 0x90, 0xa4}}
	condRemotePort = windows.GUID{Data1: 0xc35a604d, Data2: 0xd22b, Data3: 0x4e1a, Data4: [8]byte{0x91, 0xb4, 0x68, 0xf6, 0x74, 0xee, 0x67, 0x4b}}
	condRemoteAddr = windows.GUID{Data1: 0xb235ae9a, Data2: 0x1d64, Data3: 0x49b8, Data4: [8]byte{0xa4, 0x4c, 0x5f, 0xf3, 0xd9, 0x09, 0x50, 0x45}}
)

// fwpuclnt constants (fwptypes.h, fwpmtypes.h).
const (
	rpcCAuthnWinNT     = 10
	sessionFlagDynamic = 0x1
	fwpEmpty           = 0
	fwpUint8           = 1
	fwpUint16          = 2
	fwpUint32          = 3
	fwpUint64          = 4
	fwpByteBlobType    = 12
	fwpV6AddrMask      = 0x101
	matchEqual         = 0
	matchFlagsAllSet   = 6
	flagIsLoopback     = 0x1
	actionBlock        = 0x1001 // FWP_ACTION_BLOCK | FWP_ACTION_FLAG_TERMINATING
	actionPermit       = 0x1002 // FWP_ACTION_PERMIT | FWP_ACTION_FLAG_TERMINATING
	subLayerWeight     = 0xffff
)

type displayData struct {
	Name        *uint16
	Description *uint16
}

type byteBlob struct {
	Size uint32
	Data *byte
}

type session struct {
	SessionKey           windows.GUID
	DisplayData          displayData
	Flags                uint32
	TxnWaitTimeoutInMSec uint32
	ProcessID            uint32
	SID                  *windows.SID
	Username             *uint16
	KernelMode           int32
}

type subLayer struct {
	SubLayerKey  windows.GUID
	DisplayData  displayData
	Flags        uint32
	ProviderKey  *windows.GUID
	ProviderData byteBlob
	Weight       uint16
}

// value is FWP_VALUE0 / FWP_CONDITION_VALUE0: a type and an 8-byte union
// (small integers inline, everything else a pointer).
type value struct {
	Type  uint32
	Value uintptr
}

type condition struct {
	FieldKey  windows.GUID
	MatchType uint32
	Value     value
}

type action struct {
	Type uint32
	Key  windows.GUID
}

type filter struct {
	FilterKey       windows.GUID
	DisplayData     displayData
	Flags           uint32
	ProviderKey     *windows.GUID
	ProviderData    byteBlob
	LayerKey        windows.GUID
	SubLayerKey     windows.GUID
	Weight          value
	NumConditions   uint32
	Conditions      *condition
	Action          action
	ProviderContext [2]uint64 // union { UINT64 rawContext; GUID providerContextKey; }
	Reserved        *windows.GUID
	FilterID        uint64
	EffectiveWeight value
}

type v6AddrMask struct {
	Addr      [16]byte
	PrefixLen uint8
}

// call runs an fwpuclnt function (Win32 error code result). uintptrescapes:
// the Go memory behind uintptr(unsafe.Pointer(x)) arguments is moved to the
// heap and kept alive for the call, as with LazyProc.Call itself.
//
//go:uintptrescapes
func call(p *windows.LazyProc, args ...uintptr) error {
	r, _, _ := p.Call(args...)
	if r != 0 {
		return windows.Errno(r)
	}
	return nil
}

// Session is an open WFP engine with a dynamic session: its sublayer and
// filters exist until Close (or until the process ends, crash included).
type Session struct {
	mu     sync.Mutex
	engine windows.Handle
}

// Open opens a dynamic session and installs the sublayer and filters in one
// transaction. The caller must Close it.
func Open(name string, filters []Filter) (*Session, error) {
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, fmt.Errorf("wfp: %w", err)
	}
	ses := session{DisplayData: displayData{Name: n}, Flags: sessionFlagDynamic}
	var h windows.Handle
	if err := call(procEngineOpen, 0, rpcCAuthnWinNT, 0, uintptr(unsafe.Pointer(&ses)), uintptr(unsafe.Pointer(&h))); err != nil {
		return nil, fmt.Errorf("wfp: open engine: %w", err)
	}
	runtime.KeepAlive(n)
	s := &Session{engine: h}
	if err := s.install(n, filters); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Session) install(name *uint16, filters []Filter) (err error) {
	if err := call(procTransactionBegin, uintptr(s.engine), 0); err != nil {
		return fmt.Errorf("wfp: begin: %w", err)
	}
	defer func() {
		if err != nil {
			_ = call(procTransactionAbort, uintptr(s.engine))
		}
	}()
	sl := subLayer{SubLayerKey: SubLayer, DisplayData: displayData{Name: name}, Weight: subLayerWeight}
	if err := call(procSubLayerAdd, uintptr(s.engine), uintptr(unsafe.Pointer(&sl)), 0); err != nil {
		return fmt.Errorf("wfp: add sublayer: %w", err)
	}
	for _, f := range filters {
		if err := s.addFilter(f); err != nil {
			return fmt.Errorf("wfp: filter %q (%s): %w", f.Name, f.Layer, err)
		}
	}
	if err := call(procTransactionCommit, uintptr(s.engine)); err != nil {
		return fmt.Errorf("wfp: commit: %w", err)
	}
	return nil
}

// filterMem keeps an FWPM_FILTER0 and everything its condition points to
// in one block. Its address goes to FwpmFilterAdd0 as a uintptr argument
// (//go:uintptrescapes), so the block is on the heap and the uintptr values
// inside it stay valid (the Go heap does not move).
type filterMem struct {
	f     filter
	cond  condition
	luid  uint64
	mask  v6AddrMask
	name  []uint16
	appID *byteBlob
}

// addFilter builds one FWPM_FILTER0 and adds it.
func (s *Session) addFilter(f Filter) error {
	m := &filterMem{}
	var err error
	if m.name, err = windows.UTF16FromString("CrossStream: " + f.Name); err != nil {
		return err
	}
	n := 1
	switch {
	case f.App != "":
		p, err := windows.UTF16PtrFromString(f.App)
		if err != nil {
			return err
		}
		if err := call(procGetAppIDFromFile, uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&m.appID))); err != nil {
			return fmt.Errorf("app id of %s: %w", f.App, err)
		}
		defer func() { _ = call(procFreeMemory, uintptr(unsafe.Pointer(&m.appID))) }()
		m.cond = condition{FieldKey: condAppID, MatchType: matchEqual,
			Value: value{Type: fwpByteBlobType, Value: uintptr(unsafe.Pointer(m.appID))}} // C memory
	case f.Loopback:
		m.cond = condition{FieldKey: condFlags, MatchType: matchFlagsAllSet, Value: value{Type: fwpUint32, Value: flagIsLoopback}}
	case f.LocalLUID != 0:
		m.luid = f.LocalLUID
		m.cond = condition{FieldKey: condLocalIface, MatchType: matchEqual,
			Value: value{Type: fwpUint64, Value: uintptr(unsafe.Pointer(&m.luid))}}
	case f.RemotePort != 0:
		m.cond = condition{FieldKey: condRemotePort, MatchType: matchEqual, Value: value{Type: fwpUint16, Value: uintptr(f.RemotePort)}}
	case f.RemoteNet.IsValid():
		if !f.RemoteNet.Addr().Is6() {
			return errors.New("remote network must be IPv6")
		}
		m.mask = v6AddrMask{Addr: f.RemoteNet.Masked().Addr().As16(), PrefixLen: uint8(f.RemoteNet.Bits())}
		m.cond = condition{FieldKey: condRemoteAddr, MatchType: matchEqual,
			Value: value{Type: fwpV6AddrMask, Value: uintptr(unsafe.Pointer(&m.mask))}}
	default:
		n = 0
	}
	m.f = filter{
		DisplayData: displayData{Name: &m.name[0]},
		LayerKey:    layerConnectV4,
		SubLayerKey: SubLayer,
		Weight:      value{Type: fwpUint8, Value: uintptr(f.Weight)},
		Action:      action{Type: actionBlock},
	}
	if f.Layer == ConnectV6 {
		m.f.LayerKey = layerConnectV6
	}
	if f.Permit {
		m.f.Action.Type = actionPermit
	}
	if n > 0 {
		m.f.NumConditions, m.f.Conditions = 1, &m.cond
	}
	var id uint64
	err = call(procFilterAdd, uintptr(s.engine), uintptr(unsafe.Pointer(&m.f)), 0, uintptr(unsafe.Pointer(&id)))
	runtime.KeepAlive(m)
	return err
}

// Close closes the engine: the dynamic session's sublayer and filters go
// with it. Safe to call more than once.
func (s *Session) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.engine != 0 {
		_ = call(procEngineClose, uintptr(s.engine))
		s.engine = 0
	}
}

// SubLayerExists reports whether the CrossStream sublayer exists (a test
// and diagnostics helper: it opens its own short session).
func SubLayerExists() (bool, error) {
	ses := session{}
	var h windows.Handle
	if err := call(procEngineOpen, 0, rpcCAuthnWinNT, 0, uintptr(unsafe.Pointer(&ses)), uintptr(unsafe.Pointer(&h))); err != nil {
		return false, fmt.Errorf("wfp: open engine: %w", err)
	}
	defer func() { _ = call(procEngineClose, uintptr(h)) }()
	var out *subLayer
	err := call(procSubLayerGetByKey, uintptr(h), uintptr(unsafe.Pointer(&SubLayer)), uintptr(unsafe.Pointer(&out)))
	if errors.Is(err, ErrSubLayerNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("wfp: sublayer: %w", err)
	}
	_ = call(procFreeMemory, uintptr(unsafe.Pointer(&out)))
	return true, nil
}
