package ddcommon

import (
	"context"
	"fmt"
	"time"

	"github.com/dgraph-io/ristretto"
	filaddr "github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-state-types/abi"
	filabi "github.com/filecoin-project/go-state-types/abi"
	filbuild "github.com/filecoin-project/lotus/build"
	filactor "github.com/filecoin-project/lotus/chain/actors/builtin"
	filtypes "github.com/filecoin-project/lotus/chain/types"
	filactors "github.com/filecoin-project/specs-actors/actors/builtin"
	"github.com/ipfs/go-cid"
	"golang.org/x/xerrors"
)

const FilGenesisUnix = 1598306400
const FilDefaultLookback = 10

func CidV1(c cid.Cid) cid.Cid {
	if c.Version() == 1 {
		return c
	}
	return cid.NewCidV1(c.Type(), c.Hash())
}

func MainnetTime(e filabi.ChainEpoch) time.Time { return time.Unix(int64(e)*30+FilGenesisUnix, 0) }

func WallTimeEpoch() filabi.ChainEpoch {
	return abi.ChainEpoch(time.Now().Unix()-FilGenesisUnix) / filactor.EpochDurationSeconds
}

func LotusLookbackTipset(ctx context.Context) (*filtypes.TipSet, error) {
	latestHead, err := LotusAPI.ChainHead(ctx)
	if err != nil {
		return nil, xerrors.Errorf("failed getting chain head: %w", err)
	}

	wallUnix := time.Now().Unix()
	filUnix := int64(latestHead.Blocks()[0].Timestamp)

	if wallUnix < filUnix ||
		wallUnix > filUnix+int64(
			// allow up to 2 nul tipsets in a row ( 3 is virtually impossible )
			filbuild.PropagationDelaySecs+(2*filactors.EpochDurationSeconds),
		) {
		return nil, xerrors.Errorf(
			"lotus API out of sync: chainHead reports unixtime %d (height: %d) while walltime is %d (delta: %s)",
			filUnix,
			latestHead.Height(),
			wallUnix,
			time.Second*time.Duration(wallUnix-filUnix),
		)
	}

	latestHeight := latestHead.Height()

	tipsetAtLookback, err := LotusAPI.ChainGetTipSetByHeight(ctx, latestHeight-filabi.ChainEpoch(lotusLookbackEpochs), latestHead.Key())
	if err != nil {
		return nil, xerrors.Errorf("determining target tipset %d epochs ago failed: %w", lotusLookbackEpochs, err)
	}

	return tipsetAtLookback, nil
}

var providerEligibleCache, _ = ristretto.NewCache(&ristretto.Config{
	NumCounters: 1e7, BufferItems: 64,
	MaxCost: 1024,
	Cost:    func(interface{}) int64 { return 1 },
})

func SpChainIneligibleReason(ctx context.Context, sp filaddr.Address) (defIneligibleReason string, defErr error) {

	defer func() {
		if defErr != nil {
			providerEligibleCache.Del(sp.String())
			defIneligibleReason = ""
		} else {
			providerEligibleCache.SetWithTTL(sp.String(), defIneligibleReason, 1, 5*time.Minute)
		}
	}()

	if protoReason, found := providerEligibleCache.Get(sp.String()); found {
		return protoReason.(string), nil
	}

	curTipset, err := LotusLookbackTipset(ctx)
	if err != nil {
		return "", err
	}

	mbi, err := LotusAPI.MinerGetBaseInfo(ctx, sp, curTipset.Height(), curTipset.Key())
	if err != nil {
		return "", err
	}
	if mbi == nil || !mbi.EligibleForMining {
		return "MBI-ineligible", nil
	}

	ydayTipset, err := LotusAPI.ChainGetTipSetByHeight(
		ctx,
		curTipset.Height()-filactors.EpochsInDay+1, // X-2880+1
		filtypes.TipSetKey{},
	)
	if err != nil {
		return "", err
	}

	for _, ts := range []*filtypes.TipSet{curTipset, ydayTipset} {
		curMF, err := LotusAPI.StateMinerFaults(ctx, sp, ts.Key())
		if err != nil {
			return "", err
		}
		if fc, _ := curMF.Count(); fc != 0 {
			return fmt.Sprintf("%d faults at epoch %d", fc, ts.Height()), nil
		}
	}

	return "", nil
}
