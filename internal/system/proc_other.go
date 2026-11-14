//go:build !linux

package system

func hasProcStat() bool { return false }

func readProcStat() (total, idle uint64, err error) { return 0, 0, errUnsupported }

func readMemInfo() (used, total uint64, err error) { return 0, 0, errUnsupported }
