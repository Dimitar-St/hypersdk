// Copyright (C) 2023, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package integration_test

import (
	"context"
	"log"
	"testing"
	"time"

	"github.com/ava-labs/hypersdk/examples/tokenvm/actions"
	"github.com/ava-labs/hypersdk/examples/tokenvm/auth"
	"github.com/ava-labs/hypersdk/examples/tokenvm/controller"
	"github.com/ava-labs/hypersdk/examples/tokenvm/genesis"
	"github.com/ava-labs/hypersdk/examples/tokenvm/utils"
	htesting "github.com/ava-labs/hypersdk/testing"

	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/snow/choices"
	"github.com/ava-labs/avalanchego/snow/consensus/snowman"
	"github.com/ava-labs/avalanchego/snow/engine/snowman/block"
	"github.com/ava-labs/avalanchego/utils/set"
	"github.com/ava-labs/avalanchego/vms/platformvm/warp"

	ginkgo "github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	"github.com/ava-labs/hypersdk/chain"
	"github.com/ava-labs/hypersdk/codec"
	"github.com/ava-labs/hypersdk/consts"
	"github.com/ava-labs/hypersdk/crypto/ed25519"
	"github.com/ava-labs/hypersdk/pubsub"
	"github.com/ava-labs/hypersdk/rpc"
	hutils "github.com/ava-labs/hypersdk/utils"
)

func TestIntegration(t *testing.T) {
	htesting.SetController(controller.New)
	instances = make([]htesting.Instance, htesting.Vms)
	htesting.RegisterBeforeSuite(instances)
	gomega.RegisterFailHandler(ginkgo.Fail)
	ginkgo.RunSpecs(t, "toke.Vm.integration test suites")
}

var (
	priv    ed25519.PrivateKey
	factory *auth.ED25519Factory
	rsender ed25519.PublicKey
	sender  string

	priv2    ed25519.PrivateKey
	factory2 *auth.ED25519Factory
	rsender2 ed25519.PublicKey
	sender2  string

	asset1   []byte = []byte("1")
	asset1ID ids.ID
	asset2   []byte = []byte("2")
	asset2ID ids.ID
	asset3   []byte = []byte("3")
	asset3ID ids.ID

	// when used with embedded.Vm.
	genesisBytes []byte
	instances    []htesting.Instance

	networkID uint32
	gen       *genesis.Genesis
)

var _ = ginkgo.AfterSuite(func() {
	for _, iv := range instances {
		iv.JSONRPCServer.Close()
		iv.TokenJSONRPCServer.Close()
		iv.WebSocketServer.Close()
		err := iv.Vm.Shutdown(context.TODO())
		gomega.Ω(err).Should(gomega.BeNil())
	}
})

var _ = ginkgo.Describe("[Ping]", func() {
	ginkgo.It("can ping", func() {
		for _, inst := range instances {
			cli := inst.Cli
			ok, err := cli.Ping(context.Background())
			gomega.Ω(ok).Should(gomega.BeTrue())
			gomega.Ω(err).Should(gomega.BeNil())
		}
	})
})

var _ = ginkgo.Describe("[Network]", func() {
	ginkgo.It("can get network", func() {
		for _, inst := range instances {
			cli := inst.Cli
			networkID, subnetID, chainID, err := cli.Network(context.Background())
			gomega.Ω(networkID).Should(gomega.Equal(uint32(1)))
			gomega.Ω(subnetID).ShouldNot(gomega.Equal(ids.Empty))
			gomega.Ω(chainID).ShouldNot(gomega.Equal(ids.Empty))
			gomega.Ω(err).Should(gomega.BeNil())
		}
	})
})

var _ = ginkgo.Describe("[Tx Processing]", func() {
	ginkgo.It("get currently accepted block ID", func() {
		for _, inst := range instances {
			cli := inst.Cli
			_, _, _, err := cli.Accepted(context.Background())
			gomega.Ω(err).Should(gomega.BeNil())
		}
	})

	var transferTxRoot *chain.Transaction
	ginkgo.It("Gossip TransferTx to a different node", func() {
		ginkgo.By("issue TransferTx", func() {
			parser, err := instances[0].Tcli.Parser(context.Background())
			gomega.Ω(err).Should(gomega.BeNil())
			submit, transferTx, _, err := instances[0].Cli.GenerateTransaction(
				context.Background(),
				parser,
				nil,
				&actions.Transfer{
					To:    rsender2,
					Value: 100_000, // must be more than StateLockup
				},
				factory,
			)
			transferTxRoot = transferTx
			gomega.Ω(err).Should(gomega.BeNil())
			gomega.Ω(submit(context.Background())).Should(gomega.BeNil())
			gomega.Ω(instances[0].Vm.Mempool().Len(context.Background())).Should(gomega.Equal(1))
		})

		ginkgo.By("skip duplicate", func() {
			_, err := instances[0].Cli.SubmitTx(
				context.Background(),
				transferTxRoot.Bytes(),
			)
			gomega.Ω(err).To(gomega.Not(gomega.BeNil()))
		})

		ginkgo.By("send gossip from node 0 to 1", func() {
			err := instances[0].Vm.Gossiper().Force(context.TODO())
			gomega.Ω(err).Should(gomega.BeNil())
		})

		ginkgo.By("skip invalid time", func() {
			tx := chain.NewTx(
				&chain.Base{
					ChainID:   instances[0].ChainID,
					Timestamp: 0,
					MaxFee:    1000,
				},
				nil,
				&actions.Transfer{
					To:    rsender2,
					Value: 110,
				},
			)
			// Must do manual construction to avoid `tx.Sign` error (would fail with
			// 0 timestamp)
			msg, err := tx.Digest()
			gomega.Ω(err).To(gomega.BeNil())
			auth, err := factory.Sign(msg, tx.Action)
			gomega.Ω(err).To(gomega.BeNil())
			tx.Auth = auth
			p := codec.NewWriter(0, consts.MaxInt) // test codec growth
			gomega.Ω(tx.Marshal(p)).To(gomega.BeNil())
			gomega.Ω(p.Err()).To(gomega.BeNil())
			_, err = instances[0].Cli.SubmitTx(
				context.Background(),
				p.Bytes(),
			)
			gomega.Ω(err).To(gomega.Not(gomega.BeNil()))
		})

		ginkgo.By("skip duplicate (after gossip, which shouldn't clear)", func() {
			_, err := instances[0].Cli.SubmitTx(
				context.Background(),
				transferTxRoot.Bytes(),
			)
			gomega.Ω(err).To(gomega.Not(gomega.BeNil()))
		})

		ginkgo.By("receive gossip in the node 1, and signal block build", func() {
			gomega.Ω(instances[1].Vm.Builder().Force(context.TODO())).To(gomega.BeNil())
			<-instances[1].ToEngine
		})

		ginkgo.By("build block in the node 1", func() {
			ctx := context.TODO()
			blk, err := instances[1].Vm.BuildBlock(ctx)
			gomega.Ω(err).To(gomega.BeNil())

			gomega.Ω(blk.Verify(ctx)).To(gomega.BeNil())
			gomega.Ω(blk.Status()).To(gomega.Equal(choices.Processing))

			err = instances[1].Vm.SetPreference(ctx, blk.ID())
			gomega.Ω(err).To(gomega.BeNil())

			gomega.Ω(blk.Accept(ctx)).To(gomega.BeNil())
			gomega.Ω(blk.Status()).To(gomega.Equal(choices.Accepted))

			lastAccepted, err := instances[1].Vm.LastAccepted(ctx)
			gomega.Ω(err).To(gomega.BeNil())
			gomega.Ω(lastAccepted).To(gomega.Equal(blk.ID()))

			results := blk.(*chain.StatelessBlock).Results()
			gomega.Ω(results).Should(gomega.HaveLen(1))
			gomega.Ω(results[0].Success).Should(gomega.BeTrue())
			gomega.Ω(results[0].Output).Should(gomega.BeNil())

			// Unit explanation
			//
			// bandwidth: tx size
			// compute: 5 for signature, 1 for base, 1 for transfer
			// read: 2 keys reads, 1 had 0 chunks
			// create: 1 key created
			// modify: 1 cold key modified
			transferTxConsumed := chain.Dimensions{222, 7, 12, 25, 13}
			gomega.Ω(results[0].Consumed).Should(gomega.Equal(transferTxConsumed))

			// Fee explanation
			//
			// Multiply all unit consumption by 1 and sum
			gomega.Ω(results[0].Fee).Should(gomega.Equal(uint64(279)))
		})

		ginkgo.By("ensure balance is updated", func() {
			balance, err := instances[1].Tcli.Balance(context.Background(), sender, ids.Empty)
			gomega.Ω(err).To(gomega.BeNil())
			gomega.Ω(balance).To(gomega.Equal(uint64(9899721)))
			balance2, err := instances[1].Tcli.Balance(context.Background(), sender2, ids.Empty)
			gomega.Ω(err).To(gomega.BeNil())
			gomega.Ω(balance2).To(gomega.Equal(uint64(100000)))
		})
	})

	ginkgo.It("ensure multiple txs work ", func() {
		ginkgo.By("transfer funds again", func() {
			parser, err := instances[1].Tcli.Parser(context.Background())
			gomega.Ω(err).Should(gomega.BeNil())
			submit, _, _, err := instances[1].Cli.GenerateTransaction(
				context.Background(),
				parser,
				nil,
				&actions.Transfer{
					To:    rsender2,
					Value: 101,
				},
				factory,
			)
			gomega.Ω(err).Should(gomega.BeNil())
			gomega.Ω(submit(context.Background())).Should(gomega.BeNil())
			time.Sleep(2 * time.Second) // for replay test
			accept := expectBlk(instances[1])
			results := accept()
			gomega.Ω(results).Should(gomega.HaveLen(1))
			gomega.Ω(results[0].Success).Should(gomega.BeTrue())

			balance2, err := instances[1].Tcli.Balance(context.Background(), sender2, ids.Empty)
			gomega.Ω(err).To(gomega.BeNil())
			gomega.Ω(balance2).To(gomega.Equal(uint64(100101)))
		})
	})

	ginkgo.It("Test processing block handling", func() {
		var accept, accept2 func() []*chain.Result

		ginkgo.By("create processing tip", func() {
			parser, err := instances[1].Tcli.Parser(context.Background())
			gomega.Ω(err).Should(gomega.BeNil())
			submit, _, _, err := instances[1].Cli.GenerateTransaction(
				context.Background(),
				parser,
				nil,
				&actions.Transfer{
					To:    rsender2,
					Value: 200,
				},
				factory,
			)
			gomega.Ω(err).Should(gomega.BeNil())
			gomega.Ω(submit(context.Background())).Should(gomega.BeNil())
			time.Sleep(2 * time.Second) // for replay test
			accept = expectBlk(instances[1])

			submit, _, _, err = instances[1].Cli.GenerateTransaction(
				context.Background(),
				parser,
				nil,
				&actions.Transfer{
					To:    rsender2,
					Value: 201,
				},
				factory,
			)
			gomega.Ω(err).Should(gomega.BeNil())
			gomega.Ω(submit(context.Background())).Should(gomega.BeNil())
			time.Sleep(2 * time.Second) // for replay test
			accept2 = expectBlk(instances[1])
		})

		ginkgo.By("clear processing tip", func() {
			results := accept()
			gomega.Ω(results).Should(gomega.HaveLen(1))
			gomega.Ω(results[0].Success).Should(gomega.BeTrue())
			results = accept2()
			gomega.Ω(results).Should(gomega.HaveLen(1))
			gomega.Ω(results[0].Success).Should(gomega.BeTrue())
		})
	})

	ginkgo.It("ensure mempool works", func() {
		ginkgo.By("fail Gossip TransferTx to a stale node when missing previous blocks", func() {
			parser, err := instances[1].Tcli.Parser(context.Background())
			gomega.Ω(err).Should(gomega.BeNil())
			submit, _, _, err := instances[1].Cli.GenerateTransaction(
				context.Background(),
				parser,
				nil,
				&actions.Transfer{
					To:    rsender2,
					Value: 203,
				},
				factory,
			)
			gomega.Ω(err).Should(gomega.BeNil())
			gomega.Ω(submit(context.Background())).Should(gomega.BeNil())

			err = instances[1].Vm.Gossiper().Force(context.TODO())
			gomega.Ω(err).Should(gomega.BeNil())

			// mempool in 0 should be 1 (old amount), since gossip/submit failed
			gomega.Ω(instances[0].Vm.Mempool().Len(context.TODO())).Should(gomega.Equal(1))
		})
	})

	ginkgo.It("ensure unprocessed tip and replay protection works", func() {
		ginkgo.By("import accepted blocks to instance 2", func() {
			ctx := context.TODO()
			o := instances[1]
			blks := []snowman.Block{}
			next, err := o.Vm.LastAccepted(ctx)
			gomega.Ω(err).Should(gomega.BeNil())
			for {
				blk, err := o.Vm.GetBlock(ctx, next)
				gomega.Ω(err).Should(gomega.BeNil())
				blks = append([]snowman.Block{blk}, blks...)
				if blk.Height() == 1 {
					break
				}
				next = blk.Parent()
			}

			n := instances[2]
			blk1, err := n.Vm.ParseBlock(ctx, blks[0].Bytes())
			gomega.Ω(err).Should(gomega.BeNil())
			err = blk1.Verify(ctx)
			gomega.Ω(err).Should(gomega.BeNil())

			// Parse tip
			blk2, err := n.Vm.ParseBlock(ctx, blks[1].Bytes())
			gomega.Ω(err).Should(gomega.BeNil())
			blk3, err := n.Vm.ParseBlock(ctx, blks[2].Bytes())
			gomega.Ω(err).Should(gomega.BeNil())

			// Verify tip
			err = blk2.Verify(ctx)
			gomega.Ω(err).Should(gomega.BeNil())
			err = blk3.Verify(ctx)
			gomega.Ω(err).Should(gomega.BeNil())

			// Check if tx from old block would be considered a repeat on processing tip
			tx := blk2.(*chain.StatelessBlock).Txs[0]
			sblk3 := blk3.(*chain.StatelessBlock)
			sblk3t := sblk3.Timestamp().UnixMilli()
			ok, err := sblk3.IsRepeat(
				ctx,
				sblk3t-n.Vm.Rules(sblk3t).GetValidityWindow(),
				[]*chain.Transaction{tx},
				set.NewBits(),
				false,
			)
			gomega.Ω(err).Should(gomega.BeNil())
			gomega.Ω(ok.Len()).Should(gomega.Equal(1))

			// Accept tip
			err = blk1.Accept(ctx)
			gomega.Ω(err).Should(gomega.BeNil())
			err = blk2.Accept(ctx)
			gomega.Ω(err).Should(gomega.BeNil())
			err = blk3.Accept(ctx)
			gomega.Ω(err).Should(gomega.BeNil())

			// Parse another
			blk4, err := n.Vm.ParseBlock(ctx, blks[3].Bytes())
			gomega.Ω(err).Should(gomega.BeNil())
			err = blk4.Verify(ctx)
			gomega.Ω(err).Should(gomega.BeNil())
			err = blk4.Accept(ctx)
			gomega.Ω(err).Should(gomega.BeNil())

			// Check if tx from old block would be considered a repeat on accepted tip
			time.Sleep(2 * time.Second)
			gomega.Ω(n.Vm.IsRepeat(ctx, []*chain.Transaction{tx}, set.NewBits(), false).Len()).Should(gomega.Equal(1))
		})
	})

	ginkgo.It("processes valid index transactions (w/block listening)", func() {
		// Clear previous txs on instance 0
		accept := expectBlk(instances[0])
		accept() // don't care about results

		// Subscribe to blocks
		cli, err := rpc.NewWebSocketClient(
			instances[0].WebSocketServer.URL,
			rpc.DefaultHandshakeTimeout,
			pubsub.MaxPendingMessages,
			pubsub.MaxReadMessageSize,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(cli.RegisterBlocks()).Should(gomega.BeNil())

		// Wait for message to be sent
		time.Sleep(2 * pubsub.MaxMessageWait)

		// Fetch balances
		balance, err := instances[0].Tcli.Balance(context.TODO(), sender, ids.Empty)
		gomega.Ω(err).Should(gomega.BeNil())

		// Send tx
		other, err := ed25519.GeneratePrivateKey()
		gomega.Ω(err).Should(gomega.BeNil())
		transfer := &actions.Transfer{
			To:    other.PublicKey(),
			Value: 1,
		}

		parser, err := instances[0].Tcli.Parser(context.Background())
		gomega.Ω(err).Should(gomega.BeNil())
		submit, _, _, err := instances[0].Cli.GenerateTransaction(
			context.Background(),
			parser,
			nil,
			transfer,
			factory,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(submit(context.Background())).Should(gomega.BeNil())

		gomega.Ω(err).Should(gomega.BeNil())
		accept = expectBlk(instances[0])
		results := accept()
		gomega.Ω(results).Should(gomega.HaveLen(1))
		gomega.Ω(results[0].Success).Should(gomega.BeTrue())

		// Read item from connection
		blk, lresults, prices, err := cli.ListenBlock(context.TODO(), parser)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(len(blk.Txs)).Should(gomega.Equal(1))
		tx := blk.Txs[0].Action.(*actions.Transfer)
		gomega.Ω(tx.Asset).To(gomega.Equal(ids.Empty))
		gomega.Ω(tx.Value).To(gomega.Equal(uint64(1)))
		gomega.Ω(lresults).Should(gomega.Equal(results))
		gomega.Ω(prices).Should(gomega.Equal(chain.Dimensions{1, 1, 1, 1, 1}))

		// Check balance modifications are correct
		balancea, err := instances[0].Tcli.Balance(context.TODO(), sender, ids.Empty)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(balance).Should(gomega.Equal(balancea + lresults[0].Fee + 1))

		// Close connection when done
		gomega.Ω(cli.Close()).Should(gomega.BeNil())
	})

	ginkgo.It("processes valid index transactions (w/streaming verification)", func() {
		// Create streaming client
		cli, err := rpc.NewWebSocketClient(
			instances[0].WebSocketServer.URL,
			rpc.DefaultHandshakeTimeout,
			pubsub.MaxPendingMessages,
			pubsub.MaxReadMessageSize,
		)
		gomega.Ω(err).Should(gomega.BeNil())

		// Create tx
		other, err := ed25519.GeneratePrivateKey()
		gomega.Ω(err).Should(gomega.BeNil())
		transfer := &actions.Transfer{
			To:    other.PublicKey(),
			Value: 1,
		}
		parser, err := instances[0].Tcli.Parser(context.Background())
		gomega.Ω(err).Should(gomega.BeNil())
		_, tx, _, err := instances[0].Cli.GenerateTransaction(
			context.Background(),
			parser,
			nil,
			transfer,
			factory,
		)
		gomega.Ω(err).Should(gomega.BeNil())

		// Submit tx and accept block
		gomega.Ω(cli.RegisterTx(tx)).Should(gomega.BeNil())

		// Wait for message to be sent
		time.Sleep(2 * pubsub.MaxMessageWait)

		for instances[0].Vm.Mempool().Len(context.TODO()) == 0 {
			// We need to wait for mempool to be populated because issuance will
			// return as soon as bytes are on the channel.
			hutils.Outf("{{yellow}}waiting for mempool to return non-zero txs{{/}}\n")
			time.Sleep(500 * time.Millisecond)
		}
		gomega.Ω(err).Should(gomega.BeNil())
		accept := expectBlk(instances[0])
		results := accept()
		gomega.Ω(results).Should(gomega.HaveLen(1))
		gomega.Ω(results[0].Success).Should(gomega.BeTrue())

		// Read decision from connection
		txID, dErr, result, err := cli.ListenTx(context.TODO())
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(txID).Should(gomega.Equal(tx.ID()))
		gomega.Ω(dErr).Should(gomega.BeNil())
		gomega.Ω(result.Success).Should(gomega.BeTrue())
		gomega.Ω(result).Should(gomega.Equal(results[0]))

		// Close connection when done
		gomega.Ω(cli.Close()).Should(gomega.BeNil())
	})

	ginkgo.It("mint an asset that doesn't exist", func() {
		other, err := ed25519.GeneratePrivateKey()
		gomega.Ω(err).Should(gomega.BeNil())
		assetID := ids.GenerateTestID()
		parser, err := instances[0].Tcli.Parser(context.Background())
		gomega.Ω(err).Should(gomega.BeNil())
		submit, _, _, err := instances[0].Cli.GenerateTransaction(
			context.Background(),
			parser,
			nil,
			&actions.MintAsset{
				To:    other.PublicKey(),
				Asset: assetID,
				Value: 10,
			},
			factory,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(submit(context.Background())).Should(gomega.BeNil())
		accept := expectBlk(instances[0])
		results := accept()
		gomega.Ω(results).Should(gomega.HaveLen(1))
		result := results[0]
		gomega.Ω(result.Success).Should(gomega.BeFalse())
		gomega.Ω(string(result.Output)).
			Should(gomega.ContainSubstring("asset missing"))

		exists, _, _, _, _, err := instances[0].Tcli.Asset(context.TODO(), assetID)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(exists).Should(gomega.BeFalse())
	})

	ginkgo.It("create a new asset (no metadata)", func() {
		parser, err := instances[0].Tcli.Parser(context.Background())
		gomega.Ω(err).Should(gomega.BeNil())
		submit, tx, _, err := instances[0].Cli.GenerateTransaction(
			context.Background(),
			parser,
			nil,
			&actions.CreateAsset{
				Metadata: nil,
			},
			factory,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(submit(context.Background())).Should(gomega.BeNil())
		accept := expectBlk(instances[0])
		results := accept()
		gomega.Ω(results).Should(gomega.HaveLen(1))
		gomega.Ω(results[0].Success).Should(gomega.BeTrue())

		assetID := tx.ID()
		balance, err := instances[0].Tcli.Balance(context.TODO(), sender, assetID)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(balance).Should(gomega.Equal(uint64(0)))
		exists, metadata, supply, owner, warp, err := instances[0].Tcli.Asset(
			context.TODO(),
			assetID,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(exists).Should(gomega.BeTrue())
		gomega.Ω(metadata).Should(gomega.HaveLen(0))
		gomega.Ω(supply).Should(gomega.Equal(uint64(0)))
		gomega.Ω(owner).Should(gomega.Equal(sender))
		gomega.Ω(warp).Should(gomega.BeFalse())
	})

	ginkgo.It("create asset with too long of metadata", func() {
		tx := chain.NewTx(
			&chain.Base{
				ChainID:   instances[0].ChainID,
				Timestamp: hutils.UnixRMilli(-1, 5*consts.MillisecondsPerSecond),
				MaxFee:    1000,
			},
			nil,
			&actions.CreateAsset{
				Metadata: make([]byte, actions.MaxMetadataSize*2),
			},
		)
		// Must do manual construction to avoid `tx.Sign` error (would fail with
		// too large)
		msg, err := tx.Digest()
		gomega.Ω(err).To(gomega.BeNil())
		auth, err := factory.Sign(msg, tx.Action)
		gomega.Ω(err).To(gomega.BeNil())
		tx.Auth = auth
		p := codec.NewWriter(0, consts.MaxInt) // test codec growth
		gomega.Ω(tx.Marshal(p)).To(gomega.BeNil())
		gomega.Ω(p.Err()).To(gomega.BeNil())
		_, err = instances[0].Cli.SubmitTx(
			context.Background(),
			p.Bytes(),
		)
		gomega.Ω(err.Error()).Should(gomega.ContainSubstring("size is larger than limit"))
	})

	ginkgo.It("create a new asset (simple metadata)", func() {
		parser, err := instances[0].Tcli.Parser(context.Background())
		gomega.Ω(err).Should(gomega.BeNil())
		submit, tx, _, err := instances[0].Cli.GenerateTransaction(
			context.Background(),
			parser,
			nil,
			&actions.CreateAsset{
				Metadata: asset1,
			},
			factory,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(submit(context.Background())).Should(gomega.BeNil())
		accept := expectBlk(instances[0])
		results := accept()
		gomega.Ω(results).Should(gomega.HaveLen(1))
		gomega.Ω(results[0].Success).Should(gomega.BeTrue())

		asset1ID = tx.ID()
		balance, err := instances[0].Tcli.Balance(context.TODO(), sender, asset1ID)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(balance).Should(gomega.Equal(uint64(0)))

		exists, metadata, supply, owner, warp, err := instances[0].Tcli.Asset(
			context.TODO(),
			asset1ID,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(exists).Should(gomega.BeTrue())
		gomega.Ω(metadata).Should(gomega.Equal(asset1))
		gomega.Ω(supply).Should(gomega.Equal(uint64(0)))
		gomega.Ω(owner).Should(gomega.Equal(sender))
		gomega.Ω(warp).Should(gomega.BeFalse())
	})

	ginkgo.It("mint a new asset", func() {
		parser, err := instances[0].Tcli.Parser(context.Background())
		gomega.Ω(err).Should(gomega.BeNil())
		submit, _, _, err := instances[0].Cli.GenerateTransaction(
			context.Background(),
			parser,
			nil,
			&actions.MintAsset{
				To:    rsender2,
				Asset: asset1ID,
				Value: 15,
			},
			factory,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(submit(context.Background())).Should(gomega.BeNil())
		accept := expectBlk(instances[0])
		results := accept()
		gomega.Ω(results).Should(gomega.HaveLen(1))
		gomega.Ω(results[0].Success).Should(gomega.BeTrue())

		balance, err := instances[0].Tcli.Balance(context.TODO(), sender2, asset1ID)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(balance).Should(gomega.Equal(uint64(15)))
		balance, err = instances[0].Tcli.Balance(context.TODO(), sender, asset1ID)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(balance).Should(gomega.Equal(uint64(0)))

		exists, metadata, supply, owner, warp, err := instances[0].Tcli.Asset(
			context.TODO(),
			asset1ID,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(exists).Should(gomega.BeTrue())
		gomega.Ω(metadata).Should(gomega.Equal(asset1))
		gomega.Ω(supply).Should(gomega.Equal(uint64(15)))
		gomega.Ω(owner).Should(gomega.Equal(sender))
		gomega.Ω(warp).Should(gomega.BeFalse())
	})

	ginkgo.It("mint asset from wrong owner", func() {
		other, err := ed25519.GeneratePrivateKey()
		gomega.Ω(err).Should(gomega.BeNil())
		parser, err := instances[0].Tcli.Parser(context.Background())
		gomega.Ω(err).Should(gomega.BeNil())
		submit, _, _, err := instances[0].Cli.GenerateTransaction(
			context.Background(),
			parser,
			nil,
			&actions.MintAsset{
				To:    other.PublicKey(),
				Asset: asset1ID,
				Value: 10,
			},
			factory2,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(submit(context.Background())).Should(gomega.BeNil())
		accept := expectBlk(instances[0])
		results := accept()
		gomega.Ω(results).Should(gomega.HaveLen(1))
		result := results[0]
		gomega.Ω(result.Success).Should(gomega.BeFalse())
		gomega.Ω(string(result.Output)).
			Should(gomega.ContainSubstring("wrong owner"))

		exists, metadata, supply, owner, warp, err := instances[0].Tcli.Asset(
			context.TODO(),
			asset1ID,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(exists).Should(gomega.BeTrue())
		gomega.Ω(metadata).Should(gomega.Equal(asset1))
		gomega.Ω(supply).Should(gomega.Equal(uint64(15)))
		gomega.Ω(owner).Should(gomega.Equal(sender))
		gomega.Ω(warp).Should(gomega.BeFalse())
	})

	ginkgo.It("burn new asset", func() {
		parser, err := instances[0].Tcli.Parser(context.Background())
		gomega.Ω(err).Should(gomega.BeNil())
		submit, _, _, err := instances[0].Cli.GenerateTransaction(
			context.Background(),
			parser,
			nil,
			&actions.BurnAsset{
				Asset: asset1ID,
				Value: 5,
			},
			factory2,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(submit(context.Background())).Should(gomega.BeNil())
		accept := expectBlk(instances[0])
		results := accept()
		gomega.Ω(results).Should(gomega.HaveLen(1))
		gomega.Ω(results[0].Success).Should(gomega.BeTrue())

		balance, err := instances[0].Tcli.Balance(context.TODO(), sender2, asset1ID)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(balance).Should(gomega.Equal(uint64(10)))
		balance, err = instances[0].Tcli.Balance(context.TODO(), sender, asset1ID)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(balance).Should(gomega.Equal(uint64(0)))

		exists, metadata, supply, owner, warp, err := instances[0].Tcli.Asset(
			context.TODO(),
			asset1ID,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(exists).Should(gomega.BeTrue())
		gomega.Ω(metadata).Should(gomega.Equal(asset1))
		gomega.Ω(supply).Should(gomega.Equal(uint64(10)))
		gomega.Ω(owner).Should(gomega.Equal(sender))
		gomega.Ω(warp).Should(gomega.BeFalse())
	})

	ginkgo.It("burn missing asset", func() {
		parser, err := instances[0].Tcli.Parser(context.Background())
		gomega.Ω(err).Should(gomega.BeNil())
		submit, _, _, err := instances[0].Cli.GenerateTransaction(
			context.Background(),
			parser,
			nil,
			&actions.BurnAsset{
				Asset: asset1ID,
				Value: 10,
			},
			factory,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(submit(context.Background())).Should(gomega.BeNil())
		accept := expectBlk(instances[0])
		results := accept()
		gomega.Ω(results).Should(gomega.HaveLen(1))
		result := results[0]
		gomega.Ω(result.Success).Should(gomega.BeFalse())
		gomega.Ω(string(result.Output)).
			Should(gomega.ContainSubstring("invalid balance"))

		exists, metadata, supply, owner, warp, err := instances[0].Tcli.Asset(
			context.TODO(),
			asset1ID,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(exists).Should(gomega.BeTrue())
		gomega.Ω(metadata).Should(gomega.Equal(asset1))
		gomega.Ω(supply).Should(gomega.Equal(uint64(10)))
		gomega.Ω(owner).Should(gomega.Equal(sender))
		gomega.Ω(warp).Should(gomega.BeFalse())
	})

	ginkgo.It("rejects empty mint", func() {
		other, err := ed25519.GeneratePrivateKey()
		gomega.Ω(err).Should(gomega.BeNil())
		tx := chain.NewTx(
			&chain.Base{
				ChainID:   instances[0].ChainID,
				Timestamp: hutils.UnixRMilli(-1, 5*consts.MillisecondsPerSecond),
				MaxFee:    1000,
			},
			nil,
			&actions.MintAsset{
				To:    other.PublicKey(),
				Asset: asset1ID,
			},
		)
		// Must do manual construction to avoid `tx.Sign` error (would fail with
		// bad codec)
		msg, err := tx.Digest()
		gomega.Ω(err).To(gomega.BeNil())
		auth, err := factory.Sign(msg, tx.Action)
		gomega.Ω(err).To(gomega.BeNil())
		tx.Auth = auth
		p := codec.NewWriter(0, consts.MaxInt) // test codec growth
		gomega.Ω(tx.Marshal(p)).To(gomega.BeNil())
		gomega.Ω(p.Err()).To(gomega.BeNil())
		_, err = instances[0].Cli.SubmitTx(
			context.Background(),
			p.Bytes(),
		)
		gomega.Ω(err.Error()).Should(gomega.ContainSubstring("Uint64 field is not populated"))
	})

	ginkgo.It("reject max mint", func() {
		parser, err := instances[0].Tcli.Parser(context.Background())
		gomega.Ω(err).Should(gomega.BeNil())
		submit, _, _, err := instances[0].Cli.GenerateTransaction(
			context.Background(),
			parser,
			nil,
			&actions.MintAsset{
				To:    rsender2,
				Asset: asset1ID,
				Value: consts.MaxUint64,
			},
			factory,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(submit(context.Background())).Should(gomega.BeNil())
		accept := expectBlk(instances[0])
		results := accept()
		gomega.Ω(results).Should(gomega.HaveLen(1))
		result := results[0]
		gomega.Ω(result.Success).Should(gomega.BeFalse())
		gomega.Ω(string(result.Output)).
			Should(gomega.ContainSubstring("overflow"))

		balance, err := instances[0].Tcli.Balance(context.TODO(), sender2, asset1ID)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(balance).Should(gomega.Equal(uint64(10)))
		balance, err = instances[0].Tcli.Balance(context.TODO(), sender, asset1ID)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(balance).Should(gomega.Equal(uint64(0)))

		exists, metadata, supply, owner, warp, err := instances[0].Tcli.Asset(
			context.TODO(),
			asset1ID,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(exists).Should(gomega.BeTrue())
		gomega.Ω(metadata).Should(gomega.Equal(asset1))
		gomega.Ω(supply).Should(gomega.Equal(uint64(10)))
		gomega.Ω(owner).Should(gomega.Equal(sender))
		gomega.Ω(warp).Should(gomega.BeFalse())
	})

	ginkgo.It("modify an existing asset", func() {
		parser, err := instances[0].Tcli.Parser(context.Background())
		gomega.Ω(err).Should(gomega.BeNil())
		submit, _, _, err := instances[0].Cli.GenerateTransaction(
			context.Background(),
			parser,
			nil,
			&actions.ModifyAsset{
				Asset:    asset1ID,
				Metadata: []byte("blah"),
				Owner:    ed25519.EmptyPublicKey,
			},
			factory,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(submit(context.Background())).Should(gomega.BeNil())
		accept := expectBlk(instances[0])
		results := accept()
		gomega.Ω(results).Should(gomega.HaveLen(1))
		gomega.Ω(results[0].Success).Should(gomega.BeTrue())

		balance, err := instances[0].Tcli.Balance(context.TODO(), sender2, asset1ID)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(balance).Should(gomega.Equal(uint64(10)))
		balance, err = instances[0].Tcli.Balance(context.TODO(), sender, asset1ID)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(balance).Should(gomega.Equal(uint64(0)))

		exists, metadata, supply, owner, warp, err := instances[0].Tcli.Asset(
			context.TODO(),
			asset1ID,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(exists).Should(gomega.BeTrue())
		gomega.Ω(metadata).Should(gomega.Equal([]byte("blah")))
		gomega.Ω(supply).Should(gomega.Equal(uint64(10)))
		gomega.Ω(owner).Should(gomega.Equal(utils.Address(ed25519.EmptyPublicKey)))
		gomega.Ω(warp).Should(gomega.BeFalse())
	})

	ginkgo.It("modify an asset that doesn't exist", func() {
		assetID := ids.GenerateTestID()
		parser, err := instances[0].Tcli.Parser(context.Background())
		gomega.Ω(err).Should(gomega.BeNil())
		submit, _, _, err := instances[0].Cli.GenerateTransaction(
			context.Background(),
			parser,
			nil,
			&actions.ModifyAsset{
				Asset:    assetID,
				Metadata: []byte("cool"),
				Owner:    rsender,
			},
			factory,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(submit(context.Background())).Should(gomega.BeNil())
		accept := expectBlk(instances[0])
		results := accept()
		gomega.Ω(results).Should(gomega.HaveLen(1))
		result := results[0]
		gomega.Ω(result.Success).Should(gomega.BeFalse())
		gomega.Ω(string(result.Output)).
			Should(gomega.ContainSubstring("asset missing"))

		exists, _, _, _, _, err := instances[0].Tcli.Asset(context.TODO(), assetID)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(exists).Should(gomega.BeFalse())
	})

	ginkgo.It("rejects mint of native token", func() {
		other, err := ed25519.GeneratePrivateKey()
		gomega.Ω(err).Should(gomega.BeNil())
		tx := chain.NewTx(
			&chain.Base{
				ChainID:   instances[0].ChainID,
				Timestamp: hutils.UnixRMilli(-1, 5*consts.MillisecondsPerSecond),
				MaxFee:    1000,
			},
			nil,
			&actions.MintAsset{
				To:    other.PublicKey(),
				Value: 10,
			},
		)
		// Must do manual construction to avoid `tx.Sign` error (would fail with
		// bad codec)
		msg, err := tx.Digest()
		gomega.Ω(err).To(gomega.BeNil())
		auth, err := factory.Sign(msg, tx.Action)
		gomega.Ω(err).To(gomega.BeNil())
		tx.Auth = auth
		p := codec.NewWriter(0, consts.MaxInt) // test codec growth
		gomega.Ω(tx.Marshal(p)).To(gomega.BeNil())
		gomega.Ω(p.Err()).To(gomega.BeNil())
		_, err = instances[0].Cli.SubmitTx(
			context.Background(),
			p.Bytes(),
		)
		gomega.Ω(err.Error()).Should(gomega.ContainSubstring("ID field is not populated"))
	})

	ginkgo.It("mints another new asset (to self)", func() {
		parser, err := instances[0].Tcli.Parser(context.Background())
		gomega.Ω(err).Should(gomega.BeNil())
		submit, tx, _, err := instances[0].Cli.GenerateTransaction(
			context.Background(),
			parser,
			nil,
			&actions.CreateAsset{
				Metadata: asset2,
			},
			factory,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(submit(context.Background())).Should(gomega.BeNil())
		accept := expectBlk(instances[0])
		results := accept()
		gomega.Ω(results).Should(gomega.HaveLen(1))
		gomega.Ω(results[0].Success).Should(gomega.BeTrue())
		asset2ID = tx.ID()

		submit, _, _, err = instances[0].Cli.GenerateTransaction(
			context.Background(),
			parser,
			nil,
			&actions.MintAsset{
				To:    rsender,
				Asset: asset2ID,
				Value: 10,
			},
			factory,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(submit(context.Background())).Should(gomega.BeNil())
		accept = expectBlk(instances[0])
		results = accept()
		gomega.Ω(results).Should(gomega.HaveLen(1))
		gomega.Ω(results[0].Success).Should(gomega.BeTrue())

		balance, err := instances[0].Tcli.Balance(context.TODO(), sender, asset2ID)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(balance).Should(gomega.Equal(uint64(10)))
	})

	ginkgo.It("mints another new asset (to self) on another account", func() {
		parser, err := instances[0].Tcli.Parser(context.Background())
		gomega.Ω(err).Should(gomega.BeNil())
		submit, tx, _, err := instances[0].Cli.GenerateTransaction(
			context.Background(),
			parser,
			nil,
			&actions.CreateAsset{
				Metadata: asset3,
			},
			factory2,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(submit(context.Background())).Should(gomega.BeNil())
		accept := expectBlk(instances[0])
		results := accept()
		gomega.Ω(results).Should(gomega.HaveLen(1))
		gomega.Ω(results[0].Success).Should(gomega.BeTrue())
		asset3ID = tx.ID()

		submit, _, _, err = instances[0].Cli.GenerateTransaction(
			context.Background(),
			parser,
			nil,
			&actions.MintAsset{
				To:    rsender2,
				Asset: asset3ID,
				Value: 10,
			},
			factory2,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(submit(context.Background())).Should(gomega.BeNil())
		accept = expectBlk(instances[0])
		results = accept()
		gomega.Ω(results).Should(gomega.HaveLen(1))
		gomega.Ω(results[0].Success).Should(gomega.BeTrue())

		balance, err := instances[0].Tcli.Balance(context.TODO(), sender2, asset3ID)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(balance).Should(gomega.Equal(uint64(10)))
	})

	ginkgo.It("create simple order (want 3, give 2)", func() {
		parser, err := instances[0].Tcli.Parser(context.Background())
		gomega.Ω(err).Should(gomega.BeNil())
		submit, tx, _, err := instances[0].Cli.GenerateTransaction(
			context.Background(),
			parser,
			nil,
			&actions.CreateOrder{
				In:      asset3ID,
				InTick:  1,
				Out:     asset2ID,
				OutTick: 2,
				Supply:  4,
			},
			factory,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(submit(context.Background())).Should(gomega.BeNil())
		accept := expectBlk(instances[0])
		results := accept()
		gomega.Ω(results).Should(gomega.HaveLen(1))
		gomega.Ω(results[0].Success).Should(gomega.BeTrue())

		balance, err := instances[0].Tcli.Balance(context.TODO(), sender, asset2ID)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(balance).Should(gomega.Equal(uint64(6)))

		orders, err := instances[0].Tcli.Orders(context.TODO(), actions.PairID(asset3ID, asset2ID))
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(orders).Should(gomega.HaveLen(1))
		order := orders[0]
		gomega.Ω(order.ID).Should(gomega.Equal(tx.ID()))
		gomega.Ω(order.InTick).Should(gomega.Equal(uint64(1)))
		gomega.Ω(order.OutTick).Should(gomega.Equal(uint64(2)))
		gomega.Ω(order.Owner).Should(gomega.Equal(sender))
		gomega.Ω(order.Remaining).Should(gomega.Equal(uint64(4)))
	})

	ginkgo.It("create simple order with misaligned supply", func() {
		parser, err := instances[0].Tcli.Parser(context.Background())
		gomega.Ω(err).Should(gomega.BeNil())
		submit, _, _, err := instances[0].Cli.GenerateTransaction(
			context.Background(),
			parser,
			nil,
			&actions.CreateOrder{
				In:      asset2ID,
				InTick:  4,
				Out:     asset3ID,
				OutTick: 2,
				Supply:  5, // put half of balance
			},
			factory2,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(submit(context.Background())).Should(gomega.BeNil())
		accept := expectBlk(instances[0])
		results := accept()
		gomega.Ω(results).Should(gomega.HaveLen(1))
		result := results[0]
		gomega.Ω(result.Success).Should(gomega.BeFalse())
		gomega.Ω(string(result.Output)).
			Should(gomega.ContainSubstring("supply is misaligned"))
	})

	ginkgo.It("create simple order (want 2, give 3) tracked", func() {
		parser, err := instances[0].Tcli.Parser(context.Background())
		gomega.Ω(err).Should(gomega.BeNil())
		submit, tx, _, err := instances[0].Cli.GenerateTransaction(
			context.Background(),
			parser,
			nil,
			&actions.CreateOrder{
				In:      asset2ID,
				InTick:  4,
				Out:     asset3ID,
				OutTick: 1,
				Supply:  5, // put half of balance
			},
			factory2,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(submit(context.Background())).Should(gomega.BeNil())
		accept := expectBlk(instances[0])
		results := accept()
		gomega.Ω(results).Should(gomega.HaveLen(1))
		gomega.Ω(results[0].Success).Should(gomega.BeTrue())

		balance, err := instances[0].Tcli.Balance(context.TODO(), sender2, asset3ID)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(balance).Should(gomega.Equal(uint64(5)))

		orders, err := instances[0].Tcli.Orders(context.TODO(), actions.PairID(asset2ID, asset3ID))
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(orders).Should(gomega.HaveLen(1))
		order := orders[0]
		gomega.Ω(order.ID).Should(gomega.Equal(tx.ID()))
		gomega.Ω(order.InTick).Should(gomega.Equal(uint64(4)))
		gomega.Ω(order.OutTick).Should(gomega.Equal(uint64(1)))
		gomega.Ω(order.Owner).Should(gomega.Equal(sender2))
		gomega.Ω(order.Remaining).Should(gomega.Equal(uint64(5)))
	})

	ginkgo.It("create order with insufficient balance", func() {
		parser, err := instances[0].Tcli.Parser(context.Background())
		gomega.Ω(err).Should(gomega.BeNil())
		submit, _, _, err := instances[0].Cli.GenerateTransaction(
			context.Background(),
			parser,
			nil,
			&actions.CreateOrder{
				In:      asset2ID,
				InTick:  5,
				Out:     asset3ID,
				OutTick: 1,
				Supply:  5, // put half of balance
			},
			factory,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(submit(context.Background())).Should(gomega.BeNil())
		accept := expectBlk(instances[0])
		results := accept()
		gomega.Ω(results).Should(gomega.HaveLen(1))
		result := results[0]
		gomega.Ω(result.Success).Should(gomega.BeFalse())
		gomega.Ω(string(result.Output)).
			Should(gomega.ContainSubstring("invalid balance"))
	})

	ginkgo.It("fill order with misaligned value", func() {
		orders, err := instances[0].Tcli.Orders(context.TODO(), actions.PairID(asset2ID, asset3ID))
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(orders).Should(gomega.HaveLen(1))
		order := orders[0]
		owner, err := utils.ParseAddress(order.Owner)
		gomega.Ω(err).Should(gomega.BeNil())
		parser, err := instances[0].Tcli.Parser(context.Background())
		gomega.Ω(err).Should(gomega.BeNil())
		submit, _, _, err := instances[0].Cli.GenerateTransaction(
			context.Background(),
			parser,
			nil,
			&actions.FillOrder{
				Order: order.ID,
				Owner: owner,
				In:    asset2ID,
				Out:   asset3ID,
				Value: 10, // rate of this order is 4 asset2 = 1 asset3
			},
			factory,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(submit(context.Background())).Should(gomega.BeNil())
		accept := expectBlk(instances[0])
		results := accept()
		gomega.Ω(results).Should(gomega.HaveLen(1))
		result := results[0]
		gomega.Ω(result.Success).Should(gomega.BeFalse())
		gomega.Ω(string(result.Output)).
			Should(gomega.ContainSubstring("value is misaligned"))
	})

	ginkgo.It("fill order with insufficient balance", func() {
		orders, err := instances[0].Tcli.Orders(context.TODO(), actions.PairID(asset2ID, asset3ID))
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(orders).Should(gomega.HaveLen(1))
		order := orders[0]
		owner, err := utils.ParseAddress(order.Owner)
		gomega.Ω(err).Should(gomega.BeNil())
		parser, err := instances[0].Tcli.Parser(context.Background())
		gomega.Ω(err).Should(gomega.BeNil())
		submit, _, _, err := instances[0].Cli.GenerateTransaction(
			context.Background(),
			parser,
			nil,
			&actions.FillOrder{
				Order: order.ID,
				Owner: owner,
				In:    asset2ID,
				Out:   asset3ID,
				Value: 20, // rate of this order is 4 asset2 = 1 asset3
			},
			factory,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(submit(context.Background())).Should(gomega.BeNil())
		accept := expectBlk(instances[0])
		results := accept()
		gomega.Ω(results).Should(gomega.HaveLen(1))
		result := results[0]
		gomega.Ω(result.Success).Should(gomega.BeFalse())
		gomega.Ω(string(result.Output)).
			Should(gomega.ContainSubstring("invalid balance"))
	})

	ginkgo.It("fill order with sufficient balance", func() {
		orders, err := instances[0].Tcli.Orders(context.TODO(), actions.PairID(asset2ID, asset3ID))
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(orders).Should(gomega.HaveLen(1))
		order := orders[0]
		owner, err := utils.ParseAddress(order.Owner)
		gomega.Ω(err).Should(gomega.BeNil())
		parser, err := instances[0].Tcli.Parser(context.Background())
		gomega.Ω(err).Should(gomega.BeNil())
		submit, _, _, err := instances[0].Cli.GenerateTransaction(
			context.Background(),
			parser,
			nil,
			&actions.FillOrder{
				Order: order.ID,
				Owner: owner,
				In:    asset2ID,
				Out:   asset3ID,
				Value: 4, // rate of this order is 4 asset2 = 1 asset3
			},
			factory,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(submit(context.Background())).Should(gomega.BeNil())
		accept := expectBlk(instances[0])
		results := accept()
		gomega.Ω(results).Should(gomega.HaveLen(1))
		result := results[0]
		gomega.Ω(result.Success).Should(gomega.BeTrue())
		or, err := actions.UnmarshalOrderResult(result.Output)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(or.In).Should(gomega.Equal(uint64(4)))
		gomega.Ω(or.Out).Should(gomega.Equal(uint64(1)))
		gomega.Ω(or.Remaining).Should(gomega.Equal(uint64(4)))

		balance, err := instances[0].Tcli.Balance(context.TODO(), sender, asset3ID)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(balance).Should(gomega.Equal(uint64(1)))
		balance, err = instances[0].Tcli.Balance(context.TODO(), sender, asset2ID)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(balance).Should(gomega.Equal(uint64(2)))

		orders, err = instances[0].Tcli.Orders(context.TODO(), actions.PairID(asset2ID, asset3ID))
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(orders).Should(gomega.HaveLen(1))
		order = orders[0]
		gomega.Ω(order.Remaining).Should(gomega.Equal(uint64(4)))
	})

	ginkgo.It("close order with wrong owner", func() {
		orders, err := instances[0].Tcli.Orders(context.TODO(), actions.PairID(asset2ID, asset3ID))
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(orders).Should(gomega.HaveLen(1))
		order := orders[0]
		parser, err := instances[0].Tcli.Parser(context.Background())
		gomega.Ω(err).Should(gomega.BeNil())
		submit, _, _, err := instances[0].Cli.GenerateTransaction(
			context.Background(),
			parser,
			nil,
			&actions.CloseOrder{
				Order: order.ID,
				Out:   asset3ID,
			},
			factory,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(submit(context.Background())).Should(gomega.BeNil())
		accept := expectBlk(instances[0])
		results := accept()
		gomega.Ω(results).Should(gomega.HaveLen(1))
		result := results[0]
		gomega.Ω(result.Success).Should(gomega.BeFalse())
		gomega.Ω(string(result.Output)).
			Should(gomega.ContainSubstring("unauthorized"))
	})

	ginkgo.It("close order", func() {
		orders, err := instances[0].Tcli.Orders(context.TODO(), actions.PairID(asset2ID, asset3ID))
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(orders).Should(gomega.HaveLen(1))
		order := orders[0]
		parser, err := instances[0].Tcli.Parser(context.Background())
		gomega.Ω(err).Should(gomega.BeNil())
		submit, _, _, err := instances[0].Cli.GenerateTransaction(
			context.Background(),
			parser,
			nil,
			&actions.CloseOrder{
				Order: order.ID,
				Out:   asset3ID,
			},
			factory2,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(submit(context.Background())).Should(gomega.BeNil())
		accept := expectBlk(instances[0])
		results := accept()
		gomega.Ω(results).Should(gomega.HaveLen(1))
		result := results[0]
		gomega.Ω(result.Success).Should(gomega.BeTrue())

		balance, err := instances[0].Tcli.Balance(context.TODO(), sender, asset3ID)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(balance).Should(gomega.Equal(uint64(1)))
		balance, err = instances[0].Tcli.Balance(context.TODO(), sender, asset2ID)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(balance).Should(gomega.Equal(uint64(2)))
		balance, err = instances[0].Tcli.Balance(context.TODO(), sender2, asset3ID)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(balance).Should(gomega.Equal(uint64(9)))
		balance, err = instances[0].Tcli.Balance(context.TODO(), sender2, asset2ID)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(balance).Should(gomega.Equal(uint64(4)))

		orders, err = instances[0].Tcli.Orders(context.TODO(), actions.PairID(asset2ID, asset3ID))
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(orders).Should(gomega.HaveLen(0))
	})

	ginkgo.It("create simple order (want 2, give 3) tracked from another account", func() {
		parser, err := instances[0].Tcli.Parser(context.Background())
		gomega.Ω(err).Should(gomega.BeNil())
		submit, tx, _, err := instances[0].Cli.GenerateTransaction(
			context.Background(),
			parser,
			nil,
			&actions.CreateOrder{
				In:      asset2ID,
				InTick:  2,
				Out:     asset3ID,
				OutTick: 1,
				Supply:  1,
			},
			factory,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(submit(context.Background())).Should(gomega.BeNil())
		accept := expectBlk(instances[0])
		results := accept()
		gomega.Ω(results).Should(gomega.HaveLen(1))
		gomega.Ω(results[0].Success).Should(gomega.BeTrue())

		balance, err := instances[0].Tcli.Balance(context.TODO(), sender, asset3ID)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(balance).Should(gomega.Equal(uint64(0)))

		orders, err := instances[0].Tcli.Orders(context.TODO(), actions.PairID(asset2ID, asset3ID))
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(orders).Should(gomega.HaveLen(1))
		order := orders[0]
		gomega.Ω(order.ID).Should(gomega.Equal(tx.ID()))
		gomega.Ω(order.InTick).Should(gomega.Equal(uint64(2)))
		gomega.Ω(order.OutTick).Should(gomega.Equal(uint64(1)))
		gomega.Ω(order.Owner).Should(gomega.Equal(sender))
		gomega.Ω(order.Remaining).Should(gomega.Equal(uint64(1)))
	})

	ginkgo.It("fill order with more than enough value", func() {
		orders, err := instances[0].Tcli.Orders(context.TODO(), actions.PairID(asset2ID, asset3ID))
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(orders).Should(gomega.HaveLen(1))
		order := orders[0]
		owner, err := utils.ParseAddress(order.Owner)
		gomega.Ω(err).Should(gomega.BeNil())
		parser, err := instances[0].Tcli.Parser(context.Background())
		gomega.Ω(err).Should(gomega.BeNil())
		submit, _, _, err := instances[0].Cli.GenerateTransaction(
			context.Background(),
			parser,
			nil,
			&actions.FillOrder{
				Order: order.ID,
				Owner: owner,
				In:    asset2ID,
				Out:   asset3ID,
				Value: 4,
			},
			factory2,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(submit(context.Background())).Should(gomega.BeNil())
		accept := expectBlk(instances[0])
		results := accept()
		gomega.Ω(results).Should(gomega.HaveLen(1))
		result := results[0]
		gomega.Ω(result.Success).Should(gomega.BeTrue())
		or, err := actions.UnmarshalOrderResult(result.Output)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(or.In).Should(gomega.Equal(uint64(2)))
		gomega.Ω(or.Out).Should(gomega.Equal(uint64(1)))
		gomega.Ω(or.Remaining).Should(gomega.Equal(uint64(0)))

		balance, err := instances[0].Tcli.Balance(context.TODO(), sender, asset3ID)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(balance).Should(gomega.Equal(uint64(0)))
		balance, err = instances[0].Tcli.Balance(context.TODO(), sender, asset2ID)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(balance).Should(gomega.Equal(uint64(4)))
		balance, err = instances[0].Tcli.Balance(context.TODO(), sender2, asset3ID)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(balance).Should(gomega.Equal(uint64(10)))
		balance, err = instances[0].Tcli.Balance(context.TODO(), sender2, asset2ID)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(balance).Should(gomega.Equal(uint64(2)))

		orders, err = instances[0].Tcli.Orders(context.TODO(), actions.PairID(asset2ID, asset3ID))
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(orders).Should(gomega.HaveLen(0))
	})

	ginkgo.It("import warp message with nil when expected", func() {
		tx := chain.NewTx(
			&chain.Base{
				ChainID:   instances[0].ChainID,
				Timestamp: hutils.UnixRMilli(-1, 5*consts.MillisecondsPerSecond),
				MaxFee:    1000,
			},
			nil,
			&actions.ImportAsset{},
		)
		// Must do manual construction to avoid `tx.Sign` error (would fail with
		// empty warp)
		msg, err := tx.Digest()
		gomega.Ω(err).To(gomega.BeNil())
		auth, err := factory.Sign(msg, tx.Action)
		gomega.Ω(err).To(gomega.BeNil())
		tx.Auth = auth
		p := codec.NewWriter(0, consts.MaxInt) // test codec growth
		gomega.Ω(tx.Marshal(p)).To(gomega.BeNil())
		gomega.Ω(p.Err()).To(gomega.BeNil())
		_, err = instances[0].Cli.SubmitTx(
			context.Background(),
			p.Bytes(),
		)
		gomega.Ω(err.Error()).Should(gomega.ContainSubstring("expected warp message"))
	})

	ginkgo.It("import warp message empty", func() {
		wm, err := warp.NewMessage(&warp.UnsignedMessage{}, &warp.BitSetSignature{})
		gomega.Ω(err).Should(gomega.BeNil())
		tx := chain.NewTx(
			&chain.Base{
				ChainID:   instances[0].ChainID,
				Timestamp: hutils.UnixRMilli(-1, 5*consts.MillisecondsPerSecond),
				MaxFee:    1000,
			},
			wm,
			&actions.ImportAsset{},
		)
		// Must do manual construction to avoid `tx.Sign` error (would fail with
		// empty warp)
		msg, err := tx.Digest()
		gomega.Ω(err).To(gomega.BeNil())
		auth, err := factory.Sign(msg, tx.Action)
		gomega.Ω(err).To(gomega.BeNil())
		tx.Auth = auth
		p := codec.NewWriter(0, consts.MaxInt) // test codec growth
		gomega.Ω(tx.Marshal(p)).To(gomega.BeNil())
		gomega.Ω(p.Err()).To(gomega.BeNil())
		_, err = instances[0].Cli.SubmitTx(
			context.Background(),
			p.Bytes(),
		)
		gomega.Ω(err.Error()).Should(gomega.ContainSubstring("empty warp payload"))
	})

	ginkgo.It("import with wrong payload", func() {
		uwm, err := warp.NewUnsignedMessage(networkID, ids.Empty, []byte("hello"))
		gomega.Ω(err).Should(gomega.BeNil())
		wm, err := warp.NewMessage(uwm, &warp.BitSetSignature{})
		gomega.Ω(err).Should(gomega.BeNil())
		tx := chain.NewTx(
			&chain.Base{
				ChainID:   instances[0].ChainID,
				Timestamp: hutils.UnixRMilli(-1, 5*consts.MillisecondsPerSecond),
				MaxFee:    1000,
			},
			wm,
			&actions.ImportAsset{},
		)
		// Must do manual construction to avoid `tx.Sign` error (would fail with
		// invalid object)
		msg, err := tx.Digest()
		gomega.Ω(err).To(gomega.BeNil())
		auth, err := factory.Sign(msg, tx.Action)
		gomega.Ω(err).To(gomega.BeNil())
		tx.Auth = auth
		p := codec.NewWriter(0, consts.MaxInt) // test codec growth
		gomega.Ω(tx.Marshal(p)).To(gomega.BeNil())
		gomega.Ω(p.Err()).To(gomega.BeNil())
		_, err = instances[0].Cli.SubmitTx(
			context.Background(),
			p.Bytes(),
		)
		gomega.Ω(err.Error()).Should(gomega.ContainSubstring("insufficient length for input"))
	})

	ginkgo.It("import with invalid payload", func() {
		wt := &actions.WarpTransfer{}
		wtb, err := wt.Marshal()
		gomega.Ω(err).Should(gomega.BeNil())
		uwm, err := warp.NewUnsignedMessage(networkID, ids.Empty, wtb)
		gomega.Ω(err).Should(gomega.BeNil())
		wm, err := warp.NewMessage(uwm, &warp.BitSetSignature{})
		gomega.Ω(err).Should(gomega.BeNil())
		tx := chain.NewTx(
			&chain.Base{
				ChainID:   instances[0].ChainID,
				Timestamp: hutils.UnixRMilli(-1, 5*consts.MillisecondsPerSecond),
				MaxFee:    1000,
			},
			wm,
			&actions.ImportAsset{},
		)
		// Must do manual construction to avoid `tx.Sign` error (would fail with
		// invalid object)
		msg, err := tx.Digest()
		gomega.Ω(err).To(gomega.BeNil())
		auth, err := factory.Sign(msg, tx.Action)
		gomega.Ω(err).To(gomega.BeNil())
		tx.Auth = auth
		p := codec.NewWriter(0, consts.MaxInt) // test codec growth
		gomega.Ω(tx.Marshal(p)).To(gomega.BeNil())
		gomega.Ω(p.Err()).To(gomega.BeNil())
		_, err = instances[0].Cli.SubmitTx(
			context.Background(),
			p.Bytes(),
		)
		gomega.Ω(err.Error()).Should(gomega.ContainSubstring("Uint64 field is not populated"))
	})

	ginkgo.It("import with wrong destination", func() {
		wt := &actions.WarpTransfer{
			To:                 rsender,
			Asset:              ids.GenerateTestID(),
			Value:              100,
			Return:             false,
			Reward:             100,
			TxID:               ids.GenerateTestID(),
			DestinationChainID: ids.GenerateTestID(),
		}
		wtb, err := wt.Marshal()
		gomega.Ω(err).Should(gomega.BeNil())
		uwm, err := warp.NewUnsignedMessage(networkID, ids.Empty, wtb)
		gomega.Ω(err).Should(gomega.BeNil())
		wm, err := warp.NewMessage(uwm, &warp.BitSetSignature{})
		gomega.Ω(err).Should(gomega.BeNil())
		parser, err := instances[0].Tcli.Parser(context.Background())
		gomega.Ω(err).Should(gomega.BeNil())
		submit, _, _, err := instances[0].Cli.GenerateTransaction(
			context.Background(),
			parser,
			wm,
			&actions.ImportAsset{},
			factory,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(submit(context.Background())).Should(gomega.BeNil())

		// Build block with no context (should fail)
		gomega.Ω(instances[0].Vm.Builder().Force(context.TODO())).To(gomega.BeNil())
		<-instances[0].ToEngine
		blk, err := instances[0].Vm.BuildBlock(context.TODO())
		gomega.Ω(err).To(gomega.Not(gomega.BeNil()))
		gomega.Ω(blk).To(gomega.BeNil())

		// Wait for mempool to be size 1 (txs are restored async)
		for {
			if instances[0].Vm.Mempool().Len(context.Background()) > 0 {
				break
			}
			//NOTE: For it will be a normal logger. Will remove it
			log.Println("waiting for txs to be restored")
			time.Sleep(100 * time.Millisecond)
		}

		// Build block with context
		accept := expectBlkWithContext(instances[0])
		results := accept()
		gomega.Ω(results).Should(gomega.HaveLen(1))
		result := results[0]
		gomega.Ω(result.Success).Should(gomega.BeFalse())
		gomega.Ω(string(result.Output)).Should(gomega.ContainSubstring("warp verification failed"))
	})

	ginkgo.It("export native asset", func() {
		dest := ids.GenerateTestID()
		loan, err := instances[0].Tcli.Loan(context.TODO(), ids.Empty, dest)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(loan).Should(gomega.Equal(uint64(0)))

		parser, err := instances[0].Tcli.Parser(context.Background())
		gomega.Ω(err).Should(gomega.BeNil())
		submit, tx, _, err := instances[0].Cli.GenerateTransaction(
			context.Background(),
			parser,
			nil,
			&actions.ExportAsset{
				To:          rsender,
				Asset:       ids.Empty,
				Value:       100,
				Return:      false,
				Reward:      10,
				Destination: dest,
			},
			factory,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(submit(context.Background())).Should(gomega.BeNil())
		accept := expectBlk(instances[0])
		results := accept()
		gomega.Ω(results).Should(gomega.HaveLen(1))
		result := results[0]
		gomega.Ω(result.Success).Should(gomega.BeTrue())
		wt := &actions.WarpTransfer{
			To:                 rsender,
			Asset:              ids.Empty,
			Value:              100,
			Return:             false,
			Reward:             10,
			TxID:               tx.ID(),
			DestinationChainID: dest,
		}
		wtb, err := wt.Marshal()
		gomega.Ω(err).Should(gomega.BeNil())
		wm, err := warp.NewUnsignedMessage(networkID, instances[0].ChainID, wtb)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(result.WarpMessage).Should(gomega.Equal(wm))

		loan, err = instances[0].Tcli.Loan(context.TODO(), ids.Empty, dest)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(loan).Should(gomega.Equal(uint64(110)))
	})

	ginkgo.It("export native asset (invalid return)", func() {
		parser, err := instances[0].Tcli.Parser(context.Background())
		gomega.Ω(err).Should(gomega.BeNil())
		submit, _, _, err := instances[0].Cli.GenerateTransaction(
			context.Background(),
			parser,
			nil,
			&actions.ExportAsset{
				To:          rsender,
				Asset:       ids.Empty,
				Value:       100,
				Return:      true,
				Reward:      10,
				Destination: ids.GenerateTestID(),
			},
			factory,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(submit(context.Background())).Should(gomega.BeNil())
		accept := expectBlk(instances[0])
		results := accept()
		gomega.Ω(results).Should(gomega.HaveLen(1))
		result := results[0]
		gomega.Ω(result.Success).Should(gomega.BeFalse())
		gomega.Ω(string(result.Output)).Should(gomega.ContainSubstring("not warp asset"))
	})
})

func expectBlk(i htesting.Instance) func() []*chain.Result {
	ctx := context.TODO()

	// manually signal ready
	gomega.Ω(i.Vm.Builder().Force(ctx)).To(gomega.BeNil())
	// manually ack ready sig as in engine
	<-i.ToEngine

	blk, err := i.Vm.BuildBlock(ctx)
	gomega.Ω(err).To(gomega.BeNil())
	gomega.Ω(blk).To(gomega.Not(gomega.BeNil()))

	gomega.Ω(blk.Verify(ctx)).To(gomega.BeNil())
	gomega.Ω(blk.Status()).To(gomega.Equal(choices.Processing))

	err = i.Vm.SetPreference(ctx, blk.ID())
	gomega.Ω(err).To(gomega.BeNil())

	return func() []*chain.Result {
		gomega.Ω(blk.Accept(ctx)).To(gomega.BeNil())
		gomega.Ω(blk.Status()).To(gomega.Equal(choices.Accepted))

		lastAccepted, err := i.Vm.LastAccepted(ctx)
		gomega.Ω(err).To(gomega.BeNil())
		gomega.Ω(lastAccepted).To(gomega.Equal(blk.ID()))
		return blk.(*chain.StatelessBlock).Results()
	}
}

// TODO: unify with expectBlk
func expectBlkWithContext(i htesting.Instance) func() []*chain.Result {
	ctx := context.TODO()

	// manually signal ready
	gomega.Ω(i.Vm.Builder().Force(ctx)).To(gomega.BeNil())
	// manually ack ready sig as in engine
	<-i.ToEngine

	bctx := &block.Context{PChainHeight: 1}
	blk, err := i.Vm.BuildBlockWithContext(ctx, bctx)
	gomega.Ω(err).To(gomega.BeNil())
	gomega.Ω(blk).To(gomega.Not(gomega.BeNil()))
	cblk := blk.(block.WithVerifyContext)

	gomega.Ω(cblk.VerifyWithContext(ctx, bctx)).To(gomega.BeNil())
	gomega.Ω(blk.Status()).To(gomega.Equal(choices.Processing))

	err = i.Vm.SetPreference(ctx, blk.ID())
	gomega.Ω(err).To(gomega.BeNil())

	return func() []*chain.Result {
		gomega.Ω(blk.Accept(ctx)).To(gomega.BeNil())
		gomega.Ω(blk.Status()).To(gomega.Equal(choices.Accepted))

		lastAccepted, err := i.Vm.LastAccepted(ctx)
		gomega.Ω(err).To(gomega.BeNil())
		gomega.Ω(lastAccepted).To(gomega.Equal(blk.ID()))
		return blk.(*chain.StatelessBlock).Results()
	}
}
