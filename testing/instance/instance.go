package instance

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"net/http/httptest"

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
	"github.com/ava-labs/hypersdk/examples/tokenvm/genesis"
	"github.com/ava-labs/hypersdk/requester"
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

	VMName string

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

	JSONRPCServer     *httptest.Server
	BaseJSONRPCServer *httptest.Server
	WebSocketServer   *httptest.Server
	Cli               *rpc.JSONRPCClient // clients for embedded VMs
	Tcli              *rpc.JSONRPCClient
	CCli		   customRequester 
}

type customRequester struct {
	requester *requester.EndpointRequester

	networkID uint32
	chainID   ids.ID
}

// Just a form of RPC Client
func newCustomRequester(uri string, networkID uint32, chainID ids.ID) customRequester {
	uri = strings.TrimSuffix(uri, "/")
	uri += rpc.JSONRPCEndpoint
	req := requester.New(uri, VMName)
	return customRequester{req, networkID, chainID }
}

func (c *customRequester) Genesis(ctx context.Context) (*genesis.Genesis, error) {
	var resp any
	var requester requester.EndpointRequester

	err := c.requester.SendRequest(
		ctx,
		"genesis",
		nil,
		resp,
	)
	if err != nil {
		return nil, err
	}
	return resp.(*genesis.Genesis), nil
}

type BalanceArgs struct{Address string}

type BalanceReply struct{Amount uint64}

func (c *customRequester) Balance(ctx context.Context, addr string) (uint64, error) {
	var resp = new(BalanceReply) 
	err := c.requester.SendRequest(
		ctx,
		"balance",
		&BalanceArgs{	
			Address: addr,
		},
		resp,
	)
	return resp.Amount, err
}


func (i *Instance) VerifyGenesisAllocation() {
	cli := i.CCli
	g, err := cli.Genesis(context.Background())
	gomega.Ω(err).Should(gomega.BeNil())

	csupply := uint64(0)
	for _, alloc := range g.CustomAllocation {
		balance, err := cli.Balance(context.Background(), alloc.Address)
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

func (i *Instance) Initialize(app appSender, genesisBytes []byte, configBytes []byte, customHandler string, newCustomRPCCLient func()) {
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
		&app,
	)
	gomega.Expect(err).Should(gomega.BeNil())

	var hd map[string]*common.HTTPHandler
	hd, err = v.CreateHandlers(context.TODO())
	gomega.Expect(err).Should(gomega.BeNil())

	customRPCHandler := httptest.NewServer(hd[customHandler].Handler)
	webSocketHandler := httptest.NewServer(hd[rpc.WebSocketEndpoint].Handler)
	jsonRPCHandler := httptest.NewServer(hd[rpc.JSONRPCEndpoint].Handler)

	i.ChainID = snowCtx.ChainID
	i.NodeID = snowCtx.NodeID

	i.Vm = v
	i.ToEngine = toEngine
	
	i.Cli = rpc.NewJSONRPCClient(jsonRPCHandler.URL)
	i.CCli = newCustomRequester(customHandler, snowCtx.NetworkID, snowCtx.ChainID)
	
	i.BaseJSONRPCServer = customRPCHandler
	i.WebSocketServer = webSocketHandler
	i.JSONRPCServer = jsonRPCHandler

	v.ForceReady()
}

func (i *Instance) SetControlller(ctrl func() *vm.VM) {
	newController = ctrl
}

// Set up httptest.Server which contains the genesis block

var _ common.AppSender = (*appSender)(nil)

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
