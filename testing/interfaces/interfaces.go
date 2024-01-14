package interfaces

import (
	"context"
	"time"

	"github.com/ava-labs/avalanchego/database/manager"
	smblock "github.com/ava-labs/avalanchego/snow/engine/snowman/block"
	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/snow"
	"github.com/ava-labs/avalanchego/snow/consensus/snowman"
	"github.com/ava-labs/avalanchego/snow/engine/common"
	"github.com/ava-labs/avalanchego/version"
	"github.com/ava-labs/hypersdk/chain"
	"github.com/ava-labs/hypersdk/vm"
	"go.uber.org/zap"
)

type CustomGenesis interface{}

type Instance interface {
	VerifyGenesisAllocation()
	Initialize(
		ctx context.Context,
		snowCtx *snow.Context,
		manager manager.Manager,
		genesisBytes []byte,
		upgradeBytes []byte,
		configBytes []byte,
		toEngine chan<- common.Message,
		_ []*common.Fx,
		appSender common.AppSender,
	) error
	Manager() manager.Manager
	ReadState(ctx context.Context, keys [][]byte) ([][]byte, []error)
	ForceReady()
	Shutdown(ctx context.Context)
	Version(_ context.Context) (string, error)
	CreateHandlers(_ context.Context) (map[string]*common.HTTPHandler, error)
	HealthCheck(context.Context) (interface{}, error)
	GetBlock(ctx context.Context, id ids.ID) (snowman.Block, error)
	GetStatelessBlock(ctx context.Context, blkID ids.ID) (*chain.StatelessBlock, error)
	ParseBlock(ctx context.Context, source []byte) (snowman.Block, error)
	BuildBlock(ctx context.Context) (snowman.Block, error)
	BuildBlockWithContext(ctx context.Context, blockContext *smblock.Context) (snowman.Block, error)
	CreateStaticHandlers(_ context.Context) (map[string]*common.HTTPHandler, error)
	SetState(_ context.Context, state snow.State) error
	SetControlller(func() *vm.VM)
	Submit(
		ctx context.Context,
		verifySig bool,
		txs []*chain.Transaction,
	) (errs []error)
	SetPreference(_ context.Context, id ids.ID) error
	LastAccepted(_ context.Context) (ids.ID, error)
	AppGossip(ctx context.Context, nodeID ids.NodeID, msg []byte) error
	AppRequest(
		ctx context.Context,
		nodeID ids.NodeID,
		requestID uint32,
		deadline time.Time,
		request []byte,
	) error
	AppRequestFailed(ctx context.Context, nodeID ids.NodeID, requestID uint32) error
	AppResponse(
		ctx context.Context,
		nodeID ids.NodeID,
		requestID uint32,
		response []byte,
	) error
	CrossChainAppRequest(
		ctx context.Context,
		nodeID ids.ID,
		requestID uint32,
		deadline time.Time,
		request []byte,
	) error
	CrossChainAppRequestFailed(
		ctx context.Context,
		nodeID ids.ID,
		requestID uint32,
	) error
	CrossChainAppResponse(
		ctx context.Context,
		nodeID ids.ID,
		requestID uint32,
		response []byte,
	) error
	Connected(ctx context.Context, nodeID ids.NodeID, v *version.Application) error
	Disconnected(ctx context.Context, nodeID ids.NodeID) error
	VerifyHeightIndex(context.Context) error
	GetBlockIDAtHeight(_ context.Context, height uint64) (ids.ID, error)
	Fatal(msg string, fields ...zap.Field)
}

type JSONClient interface {
	Genesis() (CustomGenesis, error)
}
