// Copyright (c) 2019 Dropbox, Inc.
// Full license can be found in the LICENSE file.

package itest

import (
	"os"
	"testing"
	"time"

	"github.com/dropbox/goebpf"
	"github.com/stretchr/testify/suite"
)

const (
	testProgramFilename = "ebpf_prog/xdp1.elf"
	programsAmount      = 4
)

type xdpTestSuite struct {
	suite.Suite
}

// Basic sanity test of BPF core functionality like
// ReadElf, create maps, load / attach programs
func (ts *xdpTestSuite) TestElfLoad() {
	// This compile ELF file contains 2 BPF(XDP type) programs with 2 BPF maps
	eb := goebpf.NewDefaultEbpfSystem()
	err := eb.LoadElf(testProgramFilename)
	ts.NoError(err)
	if err != nil {
		// ELF read error.
		ts.FailNowf("Unable to read %s", testProgramFilename)
	}

	// There should be 6 BPF maps recognized by loader
	maps := eb.GetMaps()
	ts.Equal(6, len(maps))

	txcnt := maps["txcnt"].(*goebpf.EbpfMap)
	ts.NotEqual(0, txcnt.GetFd())
	ts.Equal(goebpf.MapTypePerCPUArray, txcnt.Type)
	ts.Equal(4, txcnt.KeySize)
	ts.Equal(8, txcnt.ValueSize)
	ts.Equal(100, txcnt.MaxEntries)
	ts.Equal("/sys/fs/bpf/txcnt", txcnt.PersistentPath)

	rxcnt := maps["rxcnt"].(*goebpf.EbpfMap)
	ts.NotEqual(0, rxcnt.GetFd())
	ts.Equal(goebpf.MapTypeHash, rxcnt.Type)
	ts.Equal(8, rxcnt.KeySize)
	ts.Equal(4, rxcnt.ValueSize)
	ts.Equal(50, rxcnt.MaxEntries)

	moftxcnt := maps["match_maps_tx"].(*goebpf.EbpfMap)
	ts.NotEqual(0, moftxcnt.GetFd())
	ts.Equal(goebpf.MapTypeArrayOfMaps, moftxcnt.Type)
	ts.Equal(4, moftxcnt.KeySize)
	ts.Equal(4, moftxcnt.ValueSize)
	ts.Equal(10, moftxcnt.MaxEntries)

	mofrxcnt := maps["match_maps_rx"].(*goebpf.EbpfMap)
	ts.NotEqual(0, mofrxcnt.GetFd())
	ts.Equal(goebpf.MapTypeHashOfMaps, mofrxcnt.Type)
	ts.Equal(4, mofrxcnt.KeySize)
	ts.Equal(4, mofrxcnt.ValueSize)
	ts.Equal(20, mofrxcnt.MaxEntries)
	ts.Equal("/sys/fs/bpf/match_maps_rx", mofrxcnt.PersistentPath)

	progmap := eb.GetMapByName("programs").(*goebpf.EbpfMap)
	ts.NotNil(progmap)
	ts.NotEqual(0, progmap.GetFd())
	ts.Equal(goebpf.MapTypeProgArray, progmap.Type)
	ts.Equal(4, progmap.KeySize)
	ts.Equal(4, progmap.ValueSize)
	ts.Equal(2, progmap.MaxEntries)

	// Non existing map
	nemap := eb.GetMapByName("something")
	ts.Nil(nemap)

	// Also there should few XDP eBPF programs recognized
	ts.Equal(programsAmount, len(eb.GetPrograms()))

	// Check that everything loaded correctly / load program into kernel
	var progs [programsAmount]goebpf.Program
	for index, name := range []string{"xdp0", "xdp1", "xdp_head_meta2", "xdp_root3"} {
		// Check params
		p := eb.GetProgramByName(name)
		ts.Equal(goebpf.ProgramTypeXdp, p.GetType())
		ts.Equal(name, p.GetName())
		ts.Equal("GPLv2", p.GetLicense())
		// Load into kernel
		err = p.Load()
		ts.NoError(err)
		ts.NotEqual(0, p.GetFd())
		progs[index] = p
	}

	// Try to pin program into some filesystem
	path := bpfPath + "/xdp_pin_test"
	err = progs[0].Pin(path)
	ts.NoError(err)
	ts.FileExists(path)
	os.Remove(path)

	// Non existing program
	nep := eb.GetProgramByName("something")
	ts.Nil(nep)

	// Additional test for special map type - PROGS_ARRAY
	// To be sure that we can insert prog_fd into map
	err = progmap.Update(0, progs[0].GetFd())
	ts.NoError(err)
	err = progmap.Update(1, progs[1].GetFd())
	ts.NoError(err)

	// Attach program to first (lo) interface
	// P.S. XDP does not work on "lo" interface, however, you can still attach program to it
	// which is enough to test basic BPF functionality
	err = progs[0].Attach("lo")
	ts.NoError(err)
	// Detach program
	err = progs[0].Detach()
	ts.NoError(err)

	// Unload programs (not required for real case)
	for _, p := range progs {
		err = p.Close()
		ts.NoError(err)
	}

	// Negative: close already closed program
	err = progs[0].Close()
	ts.Error(err)

	// Negative: attach to non existing interface
	err = progs[0].Attach("dummyiface")
	ts.Error(err)
}

func (ts *xdpTestSuite) TestProgramInfo() {
	// Load test program, don't attach (not required to get info)
	eb := goebpf.NewDefaultEbpfSystem()
	err := eb.LoadElf(testProgramFilename)
	ts.NoError(err)
	if err != nil {
		ts.FailNowf("Unable to read %s", testProgramFilename)
	}
	prog := eb.GetProgramByName("xdp0")
	err = prog.Load()
	ts.NoError(err)

	// Get program info by FD (NOT ID, since this program is ours)
	info, err := goebpf.GetProgramInfoByFd(prog.GetFd())
	ts.NoError(err)

	// Check base info
	ts.Equal(prog.GetName(), info.Name)
	ts.Equal(prog.GetFd(), info.Fd)
	ts.Equal(goebpf.ProgramTypeXdp, info.Type)
	ts.True(info.JitedProgramLen > 50)
	ts.True(info.XlatedProgramLen > 60)
	// Check loaded time
	now := time.Now()
	ts.True(now.Sub(info.LoadTime) < time.Second*10)

	// Check maps
	// xdp_prog1 uses only one map - array_map
	origMap := eb.GetMapByName("array_map").(*goebpf.EbpfMap)
	infoMap := info.Maps["array_map"].(*goebpf.EbpfMap)
	err = infoMap.Create()
	ts.NoError(err)
	// Check major fields (cannot compare one to one since at least fd different)
	ts.Equal(origMap.Name, infoMap.Name)
	ts.Equal(origMap.Type, infoMap.Type)
	ts.Equal(origMap.KeySize, infoMap.KeySize)
	ts.Equal(origMap.ValueSize, infoMap.ValueSize)
	ts.Equal(origMap.MaxEntries, infoMap.MaxEntries)
	// Ensure that infoMap mirrors origMap
	err = origMap.Update(0, 123)
	ts.NoError(err)
	val, err := infoMap.LookupInt(0)
	ts.NoError(err)
	ts.Equal(123, val)
}

// Run suite
func TestXdpSuite(t *testing.T) {
	suite.Run(t, new(xdpTestSuite))
}
