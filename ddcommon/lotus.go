package ddcommon

import (
	"time"

	filabi "github.com/filecoin-project/go-state-types/abi"
	lotusapi "github.com/filecoin-project/lotus/api"
	filbuild "github.com/filecoin-project/lotus/build"
	filtypes "github.com/filecoin-project/lotus/chain/types"
	filactors "github.com/filecoin-project/specs-actors/actors/builtin"
	"github.com/ipfs/go-cid"
	"github.com/urfave/cli/v2"
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

func LotusLookbackTipset(cctx *cli.Context, api *lotusapi.FullNodeStruct) (*filtypes.TipSet, error) {
	latestHead, err := api.ChainHead(cctx.Context)
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

	tipsetAtLookback, err := api.ChainGetTipSetByHeight(cctx.Context, latestHeight-filabi.ChainEpoch(cctx.Uint("lotus-lookback-epochs")), latestHead.Key())
	if err != nil {
		return nil, xerrors.Errorf("determining target tipset %d epochs ago failed: %w", cctx.Uint("lotus-lookback-epochs"), err)
	}

	return tipsetAtLookback, nil
}
