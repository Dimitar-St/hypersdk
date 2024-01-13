package interfaces

import "github.com/ava-labs/hypersdk/vm"

type Instance interface {
	VerifyGenesisAllocation()
	Initialize()
	SetControlller(func() *vm.VM)
}
