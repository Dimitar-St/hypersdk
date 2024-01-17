package testing

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net/http/httptest"
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
	"github.com/ava-labs/hypersdk/chain"
	"github.com/ava-labs/hypersdk/crypto/ed25519"
	"github.com/ava-labs/hypersdk/examples/tokenvm/genesis"
	trpc "github.com/ava-labs/hypersdk/examples/tokenvm/rpc"
	"github.com/ava-labs/hypersdk/examples/tokenvm/utils"
	"github.com/ava-labs/hypersdk/rpc"
	"github.com/ava-labs/hypersdk/vm"
	"github.com/fatih/color"
	ginkgo "github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"go.uber.org/zap"
)

var (
	logFactory logging.Factory
	log        logging.Logger

	RequestTimeout time.Duration
	Vms            int

	Instances []Instance

	newController func() *vm.VM
)


func init() {
	logFactory = logging.NewFactory(logging.Config{
		DisplayLevel: logging.Debug,
	})
	l, err := logFactory.Make("main")
	if err != nil {
		panic(err)
	}
	log = l

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
	ChainID            ids.ID
	NodeID             ids.NodeID
	Vm                 *vm.VM
	ToEngine           chan common.Message
	JSONRPCServer      *httptest.Server
	TokenJSONRPCServer *httptest.Server
	WebSocketServer    *httptest.Server
	Cli                *rpc.JSONRPCClient // clients for embedded VMs
	Tcli               *trpc.JSONRPCClient
}

func RegisterBeforeSuite(instances []Instance, configBytes []byte) {
	_ = ginkgo.BeforeSuite(func() {
		gomega.Expect(Vms).Should(gomega.BeNumerically(">", 1))

		CreateInstances(instances, configBytes)
	})
}

func CreateInstances(instances []Instance, configBytes []byte) {
	var err error
	priv, err := ed25519.GeneratePrivateKey()
	gomega.Ω(err).Should(gomega.BeNil())
	rsender := priv.PublicKey()
	sender := utils.Address(rsender)

	log.Debug(
		"generated key",
		zap.String("addr", sender),
		zap.String("pk", hex.EncodeToString(priv[:])),
	)

	priv2, err := ed25519.GeneratePrivateKey()
	gomega.Ω(err).Should(gomega.BeNil())
	rsender2 := priv2.PublicKey()
	sender2 := utils.Address(rsender2)

	log.Debug(
		"generated key",
		zap.String("addr", sender2),
		zap.String("pk", hex.EncodeToString(priv2[:])),
	)

	gen := genesis.Default()
	gen.MinUnitPrice = chain.Dimensions{1, 1, 1, 1}
	gen.MinBlockGap = 0
	gen.CustomAllocation = []*genesis.CustomAllocation{
		{
			Address: sender,
			Balance: 10_000_000,
		},
	}
	genesisBytes, err := json.Marshal(gen)
	gomega.Expect(err).Should(gomega.BeNil())

	app := &appSender{}
	for i := range instances {
		ins := createInstance(app, genesisBytes, configBytes)
		instances[i] = ins
		fmt.Println(*instances[i].Cli)
	}

	// Verify genesis allocations loaded correctly (do here otherwise test may
	// check during and it will be inaccurate)
	for _, inst := range instances {
		cli := inst.Tcli
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

	app.instances = instances
	color.Blue("created %d VMs", Vms)
}

func createInstance(app *appSender, genesisBytes []byte, configBytes []byte) Instance {
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
		[]byte(
			`{"parallelism":3, "testMode":true, "logLevel":"debug", "trackedPairs":["*"]}`,
		),
		toEngine,
		nil,
		app,
	)
	gomega.Expect(err).Should(gomega.BeNil())

	var hd map[string]*common.HTTPHandler
	hd, err = v.CreateHandlers(context.TODO())
	gomega.Expect(err).Should(gomega.BeNil())

	jsonRPCServer := httptest.NewServer(hd[rpc.JSONRPCEndpoint].Handler)
	tjsonRPCServer := httptest.NewServer(hd[trpc.JSONRPCEndpoint].Handler)
	webSocketServer := httptest.NewServer(hd[rpc.WebSocketEndpoint].Handler)
	instance := Instance{
		ChainID:            snowCtx.ChainID,
		NodeID:             snowCtx.NodeID,
		Vm:                 v,
		ToEngine:           toEngine,
		JSONRPCServer:      jsonRPCServer,
		TokenJSONRPCServer: tjsonRPCServer,
		WebSocketServer:    webSocketServer,
		Cli:                rpc.NewJSONRPCClient(jsonRPCServer.URL),
		Tcli:               trpc.NewJSONRPCClient(tjsonRPCServer.URL, snowCtx.NetworkID, snowCtx.ChainID),
	}

	v.ForceReady()

	return instance
}

// Every VM has its own constructor for the controller. 
// So this needs to be called before every test of a VM.
func SetController(ctrl func() *vm.VM) {
	newController = ctrl
}

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
