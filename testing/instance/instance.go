package instance

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/ava-labs/avalanchego/api/metrics"
	"github.com/ava-labs/avalanchego/database/manager"
	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/snow"
	"github.com/ava-labs/avalanchego/snow/engine/common"
	"github.com/ava-labs/avalanchego/snow/validators"
	"github.com/ava-labs/avalanchego/utils/crypto/bls"
	"github.com/ava-labs/avalanchego/utils/logging"
	"github.com/ava-labs/avalanchego/utils/set"
	avago_version "github.com/ava-labs/avalanchego/version"
	"github.com/ava-labs/avalanchego/vms/platformvm/warp"
	"github.com/ava-labs/hypersdk/rpc"
	_ "github.com/ava-labs/hypersdk/testing/interfaces"
	"github.com/ava-labs/hypersdk/vm"
	"github.com/onsi/gomega"
)

var (
	logFactory logging.Factory
	log        logging.Logger

	RequestTimeout time.Duration
	Vms            int

	newController func() *vm.VM
)

func initializeLoggr() {
	logFactory = logging.NewFactory(logging.Config{
		DisplayLevel: logging.Debug,
	})
	l, err := logFactory.Make("main")
	if err != nil {
		panic(err)
	}
	log = l
}

func intializeFags() {
	flag.DurationVar(
		&RequestTimeout,
		"request-timeout",
		120*time.Second,
		"timeout for transaction issuance and confirmation",
	)
	flag.IntVar(
		&Vms,
		"vms",
		3,
		"number of VMs to create",
	)
}

type Instance struct {
	ChainID  ids.ID
	NodeID   ids.NodeID
	Vm       *vm.VM
	ToEngine chan common.Message
	Handlers map[string]*common.HTTPHandler

	//JSONRPCServer      *httptest.Server
	//TokenJSONRPCServer *httptest.Server
	//WebSocketServer    *httptest.Server
	Cli  *rpc.JSONRPCClient // clients for embedded VMs
	//	Tcli *trpc.JSONRPCClient
}

func (i *Instance) VerifyGenesisAllocation() {
	cli := i.Tcli
	g, err := cli.Genesis(context.Background())
	gomega.Ω(err).Should(gomega.BeNil())

	csupply := uint64(0)
	for _, alloc := range g.CustomAllocation {
		balance, err := cli.Balance(context.Background(), alloc.Address, ids.Empty)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(balance).Should(gomega.Equal(alloc.Balance))
		csupply += alloc.Balance
	}
	// For now just skipped metadata, focus on running the tests and make general idea
	exists, _, supply, _, warp, err := cli.Asset(context.Background(), ids.Empty)
	gomega.Ω(err).Should(gomega.BeNil())
	gomega.Ω(exists).Should(gomega.BeTrue())
	//gomega.Ω(string(metadata)).Should(gomega.Equal(tconsts.Symbol))
	gomega.Ω(supply).Should(gomega.Equal(csupply))
	//gomega.Ω(owner).Should(gomega.Equal(utils.Address(ed25519.EmptyPublicKey)))
	gomega.Ω(warp).Should(gomega.BeFalse())
}

func (i *Instance) Initialize(app appSender, genesisBytes []byte, configBytes []byte) {
	nodeID := ids.GenerateTestNodeID()
	sk, err := bls.NewSecretKey()
	gomega.Expect(err).Should(gomega.BeNil())

	l, err := logFactory.Make(nodeID.String())
	gomega.Expect(err).Should(gomega.BeNil())

	dname, err := os.MkdirTemp("", fmt.Sprintf("%s-chainData", nodeID.String()))
	gomega.Expect(err).Should(gomega.BeNil())

	networkID := uint32(1)
	subnetID := ids.GenerateTestID()
	chainID := ids.GenerateTestID()

	snowCtx := &snow.Context{
		NetworkID:      networkID,
		SubnetID:       subnetID,
		ChainID:        chainID,
		NodeID:         nodeID,
		Log:            l,
		ChainDataDir:   dname,
		Metrics:        metrics.NewOptionalGatherer(),
		PublicKey:      bls.PublicFromSecretKey(sk),
		WarpSigner:     warp.NewSigner(sk, networkID, chainID),
		ValidatorState: &validators.TestState{},
	}

	toEngine := make(chan common.Message, 1)
	db := manager.NewMemDB(avago_version.CurrentDatabase)

	v := newController()
	err = v.Initialize(
		context.TODO(),
		snowCtx,
		db,
		genesisBytes,
		nil,
		configBytes,
		i.ToEngine,
		nil,
		app,
	)
	gomega.Expect(err).Should(gomega.BeNil())

	var hd map[string]*common.HTTPHandler
	hd, err = v.CreateHandlers(context.TODO())
	gomega.Expect(err).Should(gomega.BeNil())

	i.ChainID = snowCtx.ChainID
	i.NodeID = snowCtx.NodeID
	i.Vm = v
	i.ToEngine = toEngine
	i.Handlers = hd
	i.Cli = i.Cli

	v.ForceReady()
}

func (i *Instance) SetControlller(ctrl func() *vm.VM) {
	newController = ctrl
}

// Set up httptest.Server which contains the genesis block

type appSender struct {
	next      int
	instances []Instance
}

func (app *appSender) SendAppGossip(ctx context.Context, appGossipBytes []byte) error {
	n := len(app.instances)
	sender := app.instances[app.next].NodeID
	app.next++
	app.next %= n
	return app.instances[app.next].Vm.AppGossip(ctx, sender, appGossipBytes)
}

func (*appSender) SendAppRequest(context.Context, set.Set[ids.NodeID], uint32, []byte) error {
	return nil
}

func (*appSender) SendAppResponse(context.Context, ids.NodeID, uint32, []byte) error {
	return nil
}

func (*appSender) SendAppGossipSpecific(context.Context, set.Set[ids.NodeID], []byte) error {
	return nil
}

func (*appSender) SendCrossChainAppRequest(context.Context, ids.ID, uint32, []byte) error {
	return nil
}

func (*appSender) SendCrossChainAppResponse(context.Context, ids.ID, uint32, []byte) error {
	return nil
}
